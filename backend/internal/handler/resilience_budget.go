package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

// Request budget in the handler failover loops (design §6). Every helper
// keeps the 0.3.0 behaviour when the request carries no budget (switch off,
// WebSocket turns, internal calls).

const (
	// resilienceSlowAttempt makes the failure of an attempt that took this
	// long account-level: switch instead of retrying the same account.
	resilienceSlowAttempt = 60 * time.Second
	// resilienceBackoffJitter is the ±share applied to every back-off.
	resilienceBackoffJitter = 0.2
)

// resilienceHostSharer is implemented by the gateway services.
type resilienceHostSharer interface {
	ResilienceSameHostAccountIDs(ctx context.Context, accountID int64) []int64
}

func requestBudget(ctx context.Context) *resilience.Budget {
	return resilience.StateFrom(ctx).Budget()
}

// resilienceAttemptFailed records a failed upstream attempt: it counts the
// failure against the session (design §4.1) and the request budget, and
// reports whether the request must end because the budget allows no further
// attempt or the upstream refused the request content. Every failover loop
// calls it once per failed attempt, before any same-account retry or switch.
func resilienceAttemptFailed(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError) bool {
	if failoverErr != nil {
		resilience.NoteCapabilityAttempt(ctx, failoverErr.IsCapabilityStreamOnly())
	}
	if !failoverErr.IsCapabilityStreamOnly() {
		resilienceReportAttemptFailure(ctx, accountID, failoverErr)
	}
	if resilienceContentRejected(failoverErr) {
		logger.FromContext(ctx).Info("resilience.content_policy_no_failover",
			zap.Int64("account_id", accountID),
			zap.Int("upstream_status", failoverErr.StatusCode),
		)
		return true
	}
	b := requestBudget(ctx)
	if b == nil {
		return false
	}
	now := time.Now()
	b.RecordAttemptAt(now)
	if b.CanAttempt(now) {
		return false
	}
	fields := []zap.Field{
		zap.String("reason", b.Exhausted()),
		zap.Int("attempts", b.Attempts()),
		zap.Int64("account_id", accountID),
	}
	if failoverErr != nil {
		fields = append(fields, zap.Int("upstream_status", failoverErr.StatusCode))
	}
	resilience.Count(resilience.MetricBudgetExhausted, resilience.StateFrom(ctx).GroupID(), accountID, b.Exhausted())
	logger.FromContext(ctx).Warn("resilience.budget_exhausted", fields...)
	return true
}

// sameAccountRetryAllowedCtx is sameAccountRetryAllowed under the request
// budget (design §6.2): account-level failures never retry the same account,
// waits beyond the cap or the remaining budget switch instead, and each
// account gets at most budget.same_account_retry_max retries.
func sameAccountRetryAllowedCtx(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError, retryCount, retryLimit int) bool {
	if !sameAccountRetryAllowed(failoverErr, retryCount, retryLimit) {
		return false
	}
	b := requestBudget(ctx)
	if b == nil {
		return true
	}
	if resilienceAccountLevelFailure(failoverErr, b.LastAttemptDuration()) {
		return false
	}
	if wait := failoverErr.SameAccountRetryDelay; wait > 0 {
		if (b.RetryAfterCap() > 0 && wait > b.RetryAfterCap()) || b.ClampWait(wait, time.Now()) < wait {
			return false
		}
	}
	return b.AllowSameAccountRetry(accountID)
}

// resilienceAccountLevelFailure reports failures that must switch accounts
// without a same-account retry: balance, authentication, long rate limits,
// 504/524 and slow attempts.
func resilienceAccountLevelFailure(failoverErr *service.UpstreamFailoverError, attempt time.Duration) bool {
	if failoverErr == nil {
		return true
	}
	switch failoverErr.StatusCode {
	case http.StatusGatewayTimeout, 524:
		return true
	}
	if attempt >= resilienceSlowAttempt {
		return true
	}
	return service.ClassifyFailoverError(failoverErr).Hard()
}

// resilienceRetryDelay jitters a back-off by ±20 % and bounds it by the
// remaining budget.
func resilienceRetryDelay(ctx context.Context, d time.Duration) time.Duration {
	b := requestBudget(ctx)
	if b == nil || d <= 0 {
		return d
	}
	return b.ClampWait(resilience.Jitter(d, resilienceBackoffJitter), time.Now())
}

// resilienceBeforeSwitch applies the capacity-shedding rule before an
// account switch (design §6.3): at most one switch, and only to another
// upstream host; accounts on the failed host join the exclusions. It
// reports whether the request must end instead.
func resilienceBeforeSwitch(ctx context.Context, hosts any, accountID int64, failoverErr *service.UpstreamFailoverError, excluded map[int64]struct{}) bool {
	b := requestBudget(ctx)
	if b == nil || failoverErr == nil || service.ClassifyFailoverError(failoverErr).Category != resilience.CategoryCapacityShed {
		return false
	}
	if !b.AllowCapacitySwitch() {
		logger.FromContext(ctx).Warn("resilience.capacity_switch_exhausted", zap.Int64("account_id", accountID))
		return true
	}
	if sharer, ok := hosts.(resilienceHostSharer); ok && excluded != nil {
		for _, id := range sharer.ResilienceSameHostAccountIDs(ctx, accountID) {
			excluded[id] = struct{}{}
		}
	}
	return false
}

// resilienceCapacityExhausted turns an exhausted capacity-shedding failure
// into the no-available-account 503 (design §6.3): no other upstream host
// could serve the request.
func resilienceCapacityExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, status int, errType, message string) (int, string, string) {
	if failoverErr.IsCapabilityStreamOnly() {
		if c != nil && c.Request != nil && service.IsCapabilityOnlyExhaustion(c.Request.Context(), failoverErr) {
			c.Writer.Header().Del("Retry-After")
			c.Header("x-should-retry", "false")
			resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: resilience.ReasonCapabilityStreamOnly})
			return http.StatusServiceUnavailable, "api_error", service.CapabilityOnlyMessage
		}
		return http.StatusServiceUnavailable, "api_error", "No available account supports this non-streaming request"
	}
	if c == nil || c.Request == nil || failoverErr == nil || status == http.StatusServiceUnavailable ||
		requestBudget(c.Request.Context()) == nil ||
		service.ClassifyFailoverError(failoverErr).Category != resilience.CategoryCapacityShed {
		return status, errType, message
	}
	return http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable"
}

// resilienceSlotWaitTimeout bounds a slot wait by the remaining pre-output
// budget (design §6.1): a wait that outlasts the budget would leave a
// streaming client to its own idle timeout instead of a terminal answer.
// Requests without a pre-output deadline keep the wait's own timeout.
func resilienceSlotWaitTimeout(c *gin.Context, timeout time.Duration) time.Duration {
	if c == nil || c.Request == nil {
		return timeout
	}
	if rem := resilience.StateFrom(c.Request.Context()).Budget().Remaining(time.Now()); rem > 0 && rem < timeout {
		return rem
	}
	return timeout
}
