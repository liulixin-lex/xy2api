package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Accounts outside the OpenAI gateway share the bounded transient kernel,
// keyed by the selection model without applying account model mapping.
func (r *ResilienceService) getModelBreakerState() *openAIAccountModelTransientState {
	if r == nil {
		return nil
	}
	r.modelBreakerOnce.Do(func() {
		r.modelBreaker = newOpenAIAccountModelTransientState(openAIModelTransientDefaultMax)
	})
	return r.modelBreaker
}

func (r *ResilienceService) recordModelBreakerFailure(ctx context.Context, account *Account) {
	if r == nil || account == nil || account.IsOpenAICompatible() || account.IsPoolMode() {
		return
	}
	model := normalizeOpenAIAccountModelTransientModel(resilience.StateFrom(ctx).Model())
	decision := r.getModelBreakerState().recordFailure(account.ID, model, time.Now())
	if decision.Cooldown > 0 {
		r.invalidateAccountGroups(account)
		slog.Info("resilience.model_breaker", "account_id", account.ID, "platform", account.Platform,
			"model", model, "failure_streak", decision.FailureStreak, "cooldown", decision.Cooldown.String())
	}
}

func (r *ResilienceService) modelBreakerEnd(account *Account, model string, now time.Time) time.Time {
	if r == nil || account == nil || account.IsOpenAICompatible() || account.IsPoolMode() || !resilience.Current().FailureOn() {
		return time.Time{}
	}
	entry, _ := r.getModelBreakerState().readEntry(account.ID, model, now)
	if now.Before(entry.blockUntil) {
		return entry.blockUntil
	}
	return time.Time{}
}

func resilienceModelBreakerBlocks(ctx context.Context, account *Account, model string) bool {
	if resilienceLastResortGate(ctx) {
		return false
	}
	return !Resilience().modelBreakerEnd(account, model, time.Now()).IsZero()
}

// Only a previously failed model skips in-place backoff. This decision runs
// before recording the current HTTP failure, preserving a healthy account's
// retry for an isolated transient error.
func resilienceGeminiSkipInPlaceRetry(ctx context.Context, account *Account, status int, headers http.Header, body []byte) bool {
	if resilience.Current().FailureOn() && isUnrecoverableAccountFailure(status, body) {
		return true
	}
	if !resilience.Current().FailureOn() || account == nil || account.IsPoolMode() || account.IsOpenAICompatible() ||
		(status < 500 && status != http.StatusRequestTimeout) ||
		classifyUpstreamHTTP(status, headers, body, "").Category != resilience.CategoryTransient {
		return false
	}
	r := Resilience()
	if r == nil {
		return false
	}
	entry, _ := r.getModelBreakerState().readEntry(account.ID, resilience.StateFrom(ctx).Model(), time.Now())
	return entry.failureStreak >= 1
}
