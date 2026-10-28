//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestInflightReservationMarksOnlyReservedBalance(t *testing.T) {
	withHandlerResilience(t, nil)
	cache := newHandlerInflightCache(1.5)
	cfg := &config.Config{}
	cfg.Billing.InflightReservation = config.InflightReservationConfig{Enabled: true, TTLSeconds: 60, FailClosedOnUnpriced: true}
	billing := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	key := &service.APIKey{User: &service.User{ID: 19, Balance: 1.5}}
	held, err := billing.ReserveInflight(context.Background(), key.User, nil, nil, 1)
	require.NoError(t, err)
	defer held.HandlerDone()
	c, _ := newResilienceTestContext(t)
	done, err := reserveInflightBalance(c, billing, &countingEstimator{cost: 1, priced: true}, key, nil, service.InflightEstimateRequest{})
	defer done()
	require.ErrorIs(t, err, service.ErrInflightBalanceReserved)
	require.True(t, service.HasOpsClientBusinessLimited(c))
	status, _, _, retry := billingErrorDetails(err)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.InDelta(t, 5, retry, 1)

	unpriced, _ := newResilienceTestContext(t)
	_, err = reserveInflightBalance(unpriced, billing, &countingEstimator{cost: 1}, key, nil, service.InflightEstimateRequest{})
	require.ErrorIs(t, err, service.ErrBillingServiceUnavailable)
	require.False(t, service.HasOpsClientBusinessLimited(unpriced))
	// WebSocket uses the same exact-sentinel projection after the ctx wrapper.
	ws, _ := newResilienceTestContext(t)
	markInflightReservationBusinessLimit(ws, fmt.Errorf("wrapped: %w", service.ErrInflightBalanceReserved))
	require.True(t, service.HasOpsClientBusinessLimited(ws))
}

func TestRecoverableConcurrencyErrorsCarryRetryAfter(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, gateway := range []bool{false, true} {
		c, rec := newResilienceTestContext(t)
		if gateway {
			(&GatewayHandler{}).handleConcurrencyError(c, &WaitQueueFullError{}, "user", false)
		} else {
			(&OpenAIGatewayHandler{}).handleConcurrencyError(c, &ConcurrencyError{SlotType: "account"}, "account", false)
		}
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		require.Equal(t, "5", rec.Header().Get("Retry-After"))
	}
	c, rec := newResilienceTestContext(t)
	c.Request.URL.Path = EndpointResponses
	(&OpenAIGatewayHandler{}).handleConcurrencyError(c, &ConcurrencyError{}, "user", true)
	_, failure := parseResponsesFailedSSE(t, rec.Body.String())
	require.Equal(t, map[string]any{"retry-after": "5"}, failure["headers"])

	terminal, rec := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleStreamingAwareErrorWithCode(terminal, http.StatusTooManyRequests, "insufficient_quota", "insufficient_quota", "quota exhausted", false, false)
	require.Empty(t, rec.Header().Get("Retry-After"))
}

func TestLiveConcurrencyHintPreservesSuppression(t *testing.T) {
	withHandlerResilience(t, nil)
	c, rec := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).writeLiveCreateError(c, service.ErrLiveConcurrencyFull)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "5", rec.Header().Get("Retry-After"))
	c, rec = newResilienceTestContext(t)
	resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: "turn_cap"})
	applyConcurrencyRetryHint(c, http.StatusTooManyRequests, gatewayConcurrencyLimitCode)
	require.Empty(t, rec.Header().Get("Retry-After"))
}
