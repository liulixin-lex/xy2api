//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fakeRetryAdvisor struct {
	service.ModelAvailabilityDiagnoser
	wait time.Duration
}

func (f fakeRetryAdvisor) NoAccountRetryAfter(context.Context, *int64, string) time.Duration {
	return f.wait
}

func (f fakeRetryAdvisor) DiagnoseModelAvailabilityForPlatform(context.Context, *int64, string, string) service.ModelAvailabilityDiagnosis {
	return service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
}

func newResilienceTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request = req.WithContext(resilience.WithRequestState(req.Context(), resilience.NewRequestState()))
	return c, w
}

func TestNoAccountResponseCarriesRetryAfter(t *testing.T) {
	// Spec: Codex backs off instead of hammering.
	withHandlerResilience(t, nil)
	groupID := int64(28)
	c, w := newResilienceTestContext(t)
	cls := classifyNoAccountErrorFromGin(c, fakeRetryAdvisor{wait: 12 * time.Second}, &service.APIKey{GroupID: &groupID}, "grok-4", "grok-4", service.PlatformGrok)
	(&GatewayHandler{}).handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, false)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "12", w.Header().Get("Retry-After"))
	require.Equal(t, "Service temporarily unavailable. Please try again in 12s.", gjson.GetBytes(w.Body.Bytes(), "error.message").String())

	// Clamped to 2-30 s; nothing recovering -> 30.
	for _, tc := range []struct {
		wait time.Duration
		want string
	}{{0, "30"}, {500 * time.Millisecond, "2"}, {10 * time.Minute, "30"}} {
		c, w := newResilienceTestContext(t)
		cls := classifyNoAccountErrorFromGin(c, fakeRetryAdvisor{wait: tc.wait}, &service.APIKey{GroupID: &groupID}, "m", "m", service.PlatformAnthropic)
		(&GatewayHandler{}).handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, false)
		require.Equal(t, tc.want, w.Header().Get("Retry-After"))
	}
}

func TestConfigErrorRetryHeaders(t *testing.T) {
	// Spec: pricing restriction keeps 503 with Retry-After 60 and x-should-retry false.
	withHandlerResilience(t, nil)
	groupID := int64(53)
	c, w := newResilienceTestContext(t)
	resilience.StateFrom(c.Request.Context()).MarkConfigError()
	cls := classifyNoAccountErrorFromGin(c, fakeRetryAdvisor{wait: time.Second}, &service.APIKey{GroupID: &groupID}, "gpt-5.5", "gpt-5.5", service.PlatformOpenAI)
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, cls.Status, cls.ErrType, "No available accounts: channel pricing restriction", false)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "60", w.Header().Get("Retry-After"))
	require.Equal(t, "false", w.Header().Get("x-should-retry"))
	require.NotContains(t, gjson.GetBytes(w.Body.Bytes(), "error.message").String(), "try again in", "message unchanged")
}

func TestRetryHintsOffRestoresLegacy(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	groupID := int64(28)
	c, w := newResilienceTestContext(t)
	cls := classifyNoAccountErrorFromGin(c, fakeRetryAdvisor{wait: 12 * time.Second}, &service.APIKey{GroupID: &groupID}, "m", "m", service.PlatformAnthropic)
	(&GatewayHandler{}).handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, false)
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Equal(t, "Service temporarily unavailable", gjson.GetBytes(w.Body.Bytes(), "error.message").String())
}

func TestUserBalanceErrorsCarryCodes(t *testing.T) {
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	status, code, msg, _ := billingErrorDetails(service.ErrInsufficientBalance)
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, code, msg, false)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "insufficient_balance", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, "INSUFFICIENT_BALANCE", gjson.GetBytes(w.Body.Bytes(), "error.code").String())

	// Spec: concurrent requests with a small balance -> 429 reservation.
	c, w = newResilienceTestContext(t)
	status, code, msg, retry := billingErrorDetails(service.NewInflightBalanceReservedError(0.8, 0.74))
	c.Header("Retry-After", strconv.Itoa(retry))
	(&GatewayHandler{}).handleStreamingAwareError(c, status, code, msg, false)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "rate_limit_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, "concurrent_balance_reserved", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	require.Contains(t, []string{"4", "5", "6"}, w.Header().Get("Retry-After"), "5 s ±1 s jitter")
}

func TestHideUpstreamBalanceOnExhaustedFailover(t *testing.T) {
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	(&GatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusForbidden,
		ResponseBody: []byte(`{"type":"error","error":{"type":"permission_error","message":"insufficient balance"}}`),
	}, service.PlatformAnthropic, false)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "insufficient")
	require.NotEmpty(t, w.Header().Get("Retry-After"))
}

func TestExhaustedRateLimitCarriesCappedRetryAfter(t *testing.T) {
	// Spec: all-accounts-rate-limited 429 carries Retry-After clamped to 2-30 s.
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: http.Header{"Retry-After": []string{"3600"}},
		ResponseBody:    []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`),
	}, false)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "30", w.Header().Get("Retry-After"), "upstream value capped by the server")
	require.NotContains(t, gjson.GetBytes(w.Body.Bytes(), "error.message").String(), "try again in", "429 keeps its message")

	c, w = newResilienceTestContext(t)
	(&GatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}, service.PlatformAnthropic, false)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "30", w.Header().Get("Retry-After"), "nothing recovering: longest back-off")

	// Switch off: upstream value passes through as before.
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	c, w = newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: http.Header{"Retry-After": []string{"3600"}},
	}, false)
	require.Equal(t, "3600", w.Header().Get("Retry-After"))
}
