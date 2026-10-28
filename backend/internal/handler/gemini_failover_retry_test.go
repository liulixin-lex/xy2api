//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiTemporaryFailoverExhaustionReturnsRetryable503(t *testing.T) {
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	(&GatewayHandler{}).handleGeminiFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: http.Header{"Retry-After": []string{"12"}},
		ResponseBody:    []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"quota exhausted"}}`),
	})
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "12", w.Header().Get("Retry-After"))
	require.Equal(t, "Upstream service temporarily unavailable", gjson.GetBytes(w.Body.Bytes(), "error.message").String())
}

func TestGeminiModelUnsupportedExhaustionKeepsClientErrorSemantics(t *testing.T) {
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	(&GatewayHandler{}).handleGeminiFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusNotFound,
		ResponseBody: []byte(`{"error":{"message":"unknown model x"}}`),
	})
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Empty(t, w.Header().Get("Retry-After"))
}
