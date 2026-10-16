//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// budgetCtx returns a request context carrying a fresh request budget.
func budgetCtx(t *testing.T, kind resilience.RequestKind, groupID int64) context.Context {
	t.Helper()
	st := resilience.NewRequestState()
	st.SetGroup(groupID)
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), kind, time.Now()))
	return ctx
}

// hostSharingUnscheduler is a TempUnscheduler that also reports accounts on
// the same upstream host.
type hostSharingUnscheduler struct {
	mockTempUnscheduler
	sameHost map[int64][]int64
}

func (h *hostSharingUnscheduler) ResilienceSameHostAccountIDs(_ context.Context, accountID int64) []int64 {
	return h.sameHost[accountID]
}

func TestBudgetFourAttemptsAtMost(t *testing.T) {
	// Spec: four consecutive TRANSIENT failures -> no fifth attempt.
	withHandlerResilience(t, nil)
	ctx := budgetCtx(t, resilience.KindText, 7)
	fs := NewFailoverState(10, false)
	sched := &mockTempUnscheduler{}
	for i := int64(1); i <= 3; i++ {
		require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, sched, i, service.PlatformOpenAI, 0, newTestFailoverErr(http.StatusBadGateway, false, false)))
	}
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, sched, 4, service.PlatformOpenAI, 0, newTestFailoverErr(http.StatusBadGateway, false, false)))
	require.Equal(t, resilience.ExhaustedAttempts, resilience.StateFrom(ctx).Budget().Exhausted())

	// Without a budget the switch limit alone applies (0.3.0).
	fs = NewFailoverState(10, false)
	for i := int64(1); i <= 5; i++ {
		require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), sched, i, service.PlatformOpenAI, 0, newTestFailoverErr(http.StatusBadGateway, false, false)))
	}
}

func TestBudgetDeadlineEndsBeforeNextAttempt(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Budget.PreOutputBudgetSeconds = 1 })
	ctx := budgetCtx(t, resilience.KindText, 7)
	fs := NewFailoverState(10, false)
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 1, service.PlatformOpenAI, 0, newTestFailoverErr(http.StatusBadGateway, false, false)))
	require.Equal(t, resilience.ExhaustedDeadline, resilience.StateFrom(ctx).Budget().Exhausted())

	// Images have no pre-output deadline.
	ctx = budgetCtx(t, resilience.KindImage, 7)
	require.Equal(t, FailoverContinue, NewFailoverState(10, false).HandleFailoverError(ctx, &mockTempUnscheduler{}, 1, service.PlatformOpenAI, 0, newTestFailoverErr(http.StatusBadGateway, false, false)))
}

func TestBudgetBalanceNotRetriedOnSameAccount(t *testing.T) {
	// Spec: a pool-mode account answering 403 insufficient balance switches.
	withHandlerResilience(t, nil)
	ctx := budgetCtx(t, resilience.KindText, 7)
	fs := NewFailoverState(10, false)
	err := newTestFailoverErr(http.StatusForbidden, true, false)
	err.ResponseBody = []byte(`{"error":{"message":"insufficient balance"}}`)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 1, service.PlatformOpenAI, 3, err))
	require.Zero(t, fs.SameAccountRetryCount[1])
	require.Equal(t, 1, fs.SwitchCount)
	require.Contains(t, fs.FailedAccountIDs, int64(1))

	// 504 and 524 are account-level as well.
	for _, status := range []int{http.StatusGatewayTimeout, 524} {
		fs = NewFailoverState(10, false)
		require.Equal(t, FailoverContinue, fs.HandleFailoverError(budgetCtx(t, resilience.KindText, 7), &mockTempUnscheduler{}, 1, service.PlatformOpenAI, 3, newTestFailoverErr(status, true, false)))
		require.Zero(t, fs.SameAccountRetryCount[1], status)
	}

	// Without a budget the pool-mode retry happens as before.
	fs = NewFailoverState(10, false)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, 1, service.PlatformOpenAI, 3, err))
	require.Equal(t, 1, fs.SameAccountRetryCount[1])
}

func TestBudgetSameAccountRetryOncePerAccount(t *testing.T) {
	withHandlerResilience(t, nil)
	ctx := budgetCtx(t, resilience.KindText, 7)
	fs := NewFailoverState(10, false)
	sched := &mockTempUnscheduler{}
	err := newTestFailoverErr(http.StatusServiceUnavailable, true, false)
	err.SameAccountRetryDelay = time.Millisecond
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, sched, 1, service.PlatformOpenAI, 3, err))
	require.Equal(t, 1, fs.SameAccountRetryCount[1], "first failure retries the same account")
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, sched, 1, service.PlatformOpenAI, 3, err))
	require.Equal(t, 1, fs.SameAccountRetryCount[1], "at most one same-account retry")
	require.Equal(t, 1, fs.SwitchCount)
}

func TestBudgetUpstreamRetryAfterBeyondCapSwitches(t *testing.T) {
	// Spec: upstream asks for 120 s -> no wait on that account, switch.
	withHandlerResilience(t, nil)
	ctx := budgetCtx(t, resilience.KindText, 7)
	fs := NewFailoverState(10, false)
	err := newTestFailoverErr(http.StatusServiceUnavailable, true, false)
	err.SameAccountRetryDelay = 120 * time.Second
	start := time.Now()
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 1, service.PlatformOpenAI, 3, err))
	require.Less(t, time.Since(start), time.Second)
	require.Zero(t, fs.SameAccountRetryCount[1])
	require.Equal(t, 1, fs.SwitchCount)
}

func TestBudgetCapacityShedSwitchesOnceToAnotherHost(t *testing.T) {
	// Spec: shared relay overload; alternatives on the same host are skipped.
	withHandlerResilience(t, nil)
	ctx := budgetCtx(t, resilience.KindText, 7)
	fs := NewFailoverState(10, false)
	sched := &hostSharingUnscheduler{sameHost: map[int64][]int64{1: {2, 3}}}
	shed := &service.UpstreamFailoverError{StatusCode: 529, RequestScopedTransient: true}

	// No same-account retry configured: switch, excluding host H.
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, sched, 1, service.PlatformAnthropic, 0, shed))
	require.Contains(t, fs.FailedAccountIDs, int64(2))
	require.Contains(t, fs.FailedAccountIDs, int64(3))

	// Selection then finds nothing on another host: no back-off re-selection.
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(ctx))

	// A second capacity failure elsewhere ends the request.
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, sched, 9, service.PlatformAnthropic, 0, shed))
}

func TestBudgetRetryDelayJitterAndClamp(t *testing.T) {
	withHandlerResilience(t, nil)
	require.Equal(t, 2*time.Second, resilienceRetryDelay(context.Background(), 2*time.Second), "no budget: unchanged")
	ctx := budgetCtx(t, resilience.KindText, 7)
	for i := 0; i < 50; i++ {
		d := resilienceRetryDelay(ctx, 2*time.Second)
		require.GreaterOrEqual(t, d, 1600*time.Millisecond)
		require.LessOrEqual(t, d, 2400*time.Millisecond)
	}
}

func TestCapacityExhaustedBecomesNoAccount503(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	c, _ := newResilienceTestContext(t)
	c.Request = c.Request.WithContext(budgetCtx(t, resilience.KindText, 7))
	shed := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, RequestScopedTransient: true}
	status, errType, msg := resilienceCapacityExhausted(c, shed, http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable")
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "api_error", errType)
	require.Equal(t, "Service temporarily unavailable", msg)

	// Other failures and requests without a budget keep their mapping.
	status, _, _ = resilienceCapacityExhausted(c, newTestFailoverErr(http.StatusBadGateway, false, false), http.StatusBadGateway, "upstream_error", "x")
	require.Equal(t, http.StatusBadGateway, status)
	c2, _ := newResilienceTestContext(t)
	status, _, _ = resilienceCapacityExhausted(c2, shed, http.StatusBadGateway, "upstream_error", "x")
	require.Equal(t, http.StatusBadGateway, status)
}

// countingRecorder counts session failures per account.
type countingRecorder struct{ failures map[int64]int }

func (r *countingRecorder) Failure(_ context.Context, _ int64, _ string, accountID int64, c resilience.Category) {
	if c.CountsForSession() {
		r.failures[accountID]++
	}
}
func (r *countingRecorder) Success(context.Context, int64, string, int64) {}

func TestEveryFailoverLoopReportsSessionFailures(t *testing.T) {
	// The OpenAI handlers run their own loops and only call
	// resilienceAttemptFailed; the shared loop calls HandleFailoverError.
	// Both must count each failed attempt once against the session.
	gin.SetMode(gin.TestMode)
	ctx := budgetCtx(t, resilience.KindText, 7)
	rec := &countingRecorder{failures: map[int64]int{}}
	resilience.StateFrom(ctx).SetSession(7, "sess", rec)
	bad := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}

	resilienceAttemptFailed(ctx, 11, bad)
	require.Equal(t, 1, rec.failures[11], "OpenAI loop")

	fs := NewFailoverState(3, false)
	fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 12, service.PlatformAnthropic, 0, bad)
	require.Equal(t, 1, rec.failures[12], "shared loop, counted once")
}

func TestFailoverLoopSkipsAttemptReportedByStream(t *testing.T) {
	// A failure after output is counted by the stream handler (design
	// §7.6); when its error also reaches a failover loop it counts once.
	gin.SetMode(gin.TestMode)
	ctx := budgetCtx(t, resilience.KindText, 7)
	rec := &countingRecorder{failures: map[int64]int{}}
	st := resilience.StateFrom(ctx)
	st.SetSession(7, "sess", rec)
	bad := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}

	require.True(t, st.ReportStreamFailure(13, bad))
	st.SessionFailure(ctx, 13, resilience.CategoryTransient)
	resilienceAttemptFailed(ctx, 13, bad)
	fs := NewFailoverState(3, false)
	fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 13, service.PlatformAnthropic, 0, bad)
	require.Equal(t, 1, rec.failures[13])

	// Other accounts of the request still count.
	resilienceAttemptFailed(ctx, 14, bad)
	require.Equal(t, 1, rec.failures[14])
}
