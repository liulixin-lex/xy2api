//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type ordinaryReservationCache struct {
	*handlerInflightCache
	calls int
}

func (m *ordinaryReservationCache) ReserveInflightBalance(ctx context.Context, user int64, id string, amount, balance float64, ttl time.Duration) (bool, float64, error) {
	m.calls++
	return m.handlerInflightCache.ReserveInflightBalance(ctx, user, id, amount, balance, ttl)
}

type ordinaryReservationRatioStore struct {
	service.ResilienceStore
	writes int
}

func (s *ordinaryReservationRatioStore) SetSessionCacheRatio(context.Context, string, float64, time.Duration) error {
	s.writes++
	return nil
}

func TestResponsesRetainsOrdinaryInflightReservation(t *testing.T) {
	withHandlerResilience(t, nil)
	f := newHandlerCompatFixture(t)
	f.account.Extra = map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileOff}
	f.handler.cfg.Billing.InflightReservation = config.InflightReservationConfig{Enabled: true, TTLSeconds: 60, FailClosedOnCacheError: true}
	cache := &ordinaryReservationCache{handlerInflightCache: newHandlerInflightCache(100)}
	billingConfig := *f.handler.cfg
	billingConfig.RunMode = ""
	billing := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, &billingConfig, nil)
	t.Cleanup(billing.Stop)
	f.handler.billingCacheService = billing
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":90,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	t.Cleanup(upstream.Close)
	f.account.Credentials["base_url"] = upstream.URL
	rec := httptest.NewRecorder()
	c := f.call("responses", `{"model":"claude-opus-5-5","max_output_tokens":64,"prompt_cache_key":"ordinary-cache-session","input":[{"role":"user","content":"hello"}]}`, rec)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, 1, cache.calls, "fixture must create a real inflight reservation")
	ratioStore := &ordinaryReservationRatioStore{}
	service.NewResilienceService(nil, ratioStore).NoteSessionCacheUsage(c.Request.Context(), 90, 100)
	t.Logf("reservation calls=%d, context reservation present=%t, cache-ratio writes=%d", cache.calls, service.InflightReservationFromContext(c.Request.Context()) != nil, ratioStore.writes)
	require.Equal(t, 1, ratioStore.writes, "billed cache-read share must still reach the per-session estimate")
	require.NotNil(t, service.InflightReservationFromContext(c.Request.Context()), "request context must retain the reservation for usage/cache-ratio recording")
}
