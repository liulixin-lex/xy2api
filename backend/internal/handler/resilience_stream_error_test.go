//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// relayConcurrencyLimit is the in-stream error a Claude relay sent after
// about 30 s in its queue (R7, production).
func relayConcurrencyLimit() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:   http.StatusTooManyRequests,
		ResponseBody: []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"gateway_concurrency_limit","message":"Concurrency limit exceeded for account, please retry later"}}`),
	}
}

func TestStreamRateLimitSwitchesAndAnswersRateLimited(t *testing.T) {
	withHandlerResilience(t, nil)

	// Before output the rate limit switches accounts like an HTTP 429.
	fs := NewFailoverState(10, false)
	ctx := budgetCtx(t, resilience.KindText, 64)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 254, service.PlatformAnthropic, 3, relayConcurrencyLimit()))
	require.Equal(t, 1, fs.SwitchCount)
	require.Contains(t, fs.FailedAccountIDs, int64(254))

	// Exhausted: a 429 with Retry-After instead of 502 "Upstream access forbidden".
	c, w := newResilienceTestContext(t)
	(&GatewayHandler{}).handleFailoverExhausted(c, relayConcurrencyLimit(), service.PlatformAnthropic, false)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "rate_limit_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.NotEmpty(t, w.Header().Get("Retry-After"))
	require.NotContains(t, w.Body.String(), "forbidden")

	// After output: the stream ends with a rate-limit error frame that tells
	// the client when to retry.
	c, w = newResilienceTestContext(t)
	_, _ = c.Writer.WriteString("event: message_start\ndata: {}\n\n")
	(&GatewayHandler{}).handleFailoverExhausted(c, relayConcurrencyLimit(), service.PlatformAnthropic, true)
	require.Contains(t, w.Body.String(), `"type":"rate_limit_error"`)
	require.Contains(t, w.Body.String(), "Please try again in")
	require.NotContains(t, w.Body.String(), "forbidden")
}
