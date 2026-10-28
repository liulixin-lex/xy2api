//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHardFailureRetryVetoDoesNotDependOnBudget(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		withHandlerResilience(t, func(s *resilience.Settings) { s.Enabled = enabled })
		for _, ctx := range []context.Context{context.Background(), budgetCtx(t, resilience.KindText, 7)} {
			for _, failure := range []*service.UpstreamFailoverError{
				{StatusCode: 400, ResponseBody: []byte(`{"error":{"message":"insufficient balance"}}`)},
				{StatusCode: 401, ResponseBody: []byte(`{"error":{"code":"invalid_api_key"}}`)},
				{StatusCode: 400, ResponseBody: []byte(`{"error":{"message":"unknown model x"}}`)},
			} {
				failure.RetryableOnSameAccount = true
				require.Equal(t, !enabled, sameAccountRetryAllowedCtx(ctx, 1, failure, 0, 3))
			}
		}
		for _, status := range []int{502, 529, 504} {
			require.True(t, sameAccountRetryAllowedCtx(context.Background(), 1, &service.UpstreamFailoverError{StatusCode: status, RetryableOnSameAccount: true}, 0, 3))
		}
	}
}

func TestGeminiExhaustionHonorsErrorsSwitchAndSuppression(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = enabled })
		c, w := newResilienceTestContext(t)
		if enabled {
			resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: "turn_cap"})
		}
		(&GatewayHandler{}).handleGeminiFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: 502, ResponseHeaders: http.Header{"Retry-After": {"90"}},
		})
		wantStatus := 502
		if enabled {
			wantStatus = 503
		}
		require.Equal(t, wantStatus, w.Code)
		require.Empty(t, w.Header().Get("Retry-After"))
	}
}

func TestGoogleConcurrencyRetryHintsPreserveExistingAndSuppression(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name     string
		err      error
		existing string
		suppress bool
		want     string
	}{
		{"queue", &WaitQueueFullError{SlotType: "account"}, "", false, "5"},
		{"user", &ConcurrencyError{SlotType: "user"}, "", false, "5"},
		{"account existing", &ConcurrencyError{SlotType: "account"}, "8", false, "8"},
		{"suppressed", &ConcurrencyError{SlotType: "user"}, "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := newResilienceTestContext(t)
			if tc.existing != "" {
				c.Header("Retry-After", tc.existing)
			}
			if tc.suppress {
				resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true})
			}
			googleConcurrencyError(c, tc.err, "user")
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.Equal(t, tc.want, w.Header().Get("Retry-After"))
		})
	}
}
