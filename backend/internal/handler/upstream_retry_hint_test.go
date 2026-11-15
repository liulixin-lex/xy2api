//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestM8RetryNotBefore(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name    string
		headers http.Header
		body    string
		want    time.Duration
		allowed bool
	}{
		{"long_seconds", http.Header{"Retry-After": {"310"}}, "", 310 * time.Second, false},
		{"long_ms", http.Header{"Retry-After-Ms": {"309000"}}, "", 309 * time.Second, false},
		{"latest", http.Header{"Retry-After": {"5"}, "Retry-After-Ms": {"15000"}}, `{"error":{"retry_after":"20"}}`, 20 * time.Second, true},
		{"event", nil, `{"response":{"error":{"headers":{"retry-after-ms":2500}}}}`, 2500 * time.Millisecond, true},
		{"body", nil, `{"error":{"retry_after_ms":2000}}`, 2 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := budgetCtx(t, resilience.KindText, 7)
			st := resilience.StateFrom(ctx)
			now := time.Now()
			hint, ok := resilience.ParseUpstreamRecoveryHint(tc.headers, []byte(tc.body), now)
			require.True(t, ok)
			require.Equal(t, tc.want, hint.Delay)
			failure := &service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, UpstreamRecoveryHint: &hint}
			st.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: 11, Protocol: "responses", Origin: "https://upstream.example"})
			require.False(t, resilienceAttemptFailed(ctx, 11, failure))
			require.Equal(t, hint.NotBefore, failure.UpstreamRecoveryHint.NotBefore)
			require.Equal(t, int64(11), failure.UpstreamTarget.AccountID)
			require.Equal(t, tc.allowed, sameAccountRetryAllowedCtx(ctx, 11, failure, 0, 1))
			wait := resilienceRetryDelay(ctx, sameAccountRetryDelayFor(failure, 1))
			require.False(t, time.Now().Add(wait).Before(hint.NotBefore), "no downward jitter or cap shortening")
			st.MarkOpsUpstreamSendBoundary(11)
			require.Nil(t, st.UpstreamRecoveryHint())
			stored, ok := st.AccountRetryNotBefore(11)
			require.True(t, ok)
			require.Equal(t, hint.NotBefore, stored.NotBefore)
		})
	}
	t.Run("save_before_budget_exhaustion", func(t *testing.T) {
		ctx := budgetCtx(t, resilience.KindText, 7)
		st := resilience.StateFrom(ctx)
		for range st.Budget().MaxAttempts() {
			st.Budget().RecordAttemptAt(time.Now())
		}
		hint := resilience.UpstreamRecoveryHint{ObservedAt: time.Now(), NotBefore: time.Now().Add(5 * time.Second)}
		failure := &service.UpstreamFailoverError{StatusCode: 503, UpstreamRecoveryHint: &hint}
		require.True(t, resilienceAttemptFailed(ctx, 11, failure))
		stored, ok := st.AccountRetryNotBefore(11)
		require.True(t, ok)
		require.Equal(t, hint.NotBefore, stored.NotBefore)
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(budgetCtx(t, resilience.KindText, 7))
		cancel()
		require.False(t, sameAccountRetryAllowedCtx(ctx, 11, &service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true}, 0, 1))
	})
}
