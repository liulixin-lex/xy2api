//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/model"
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

func TestExhaustedBadGatewayCapsUpstreamRetryAfter(t *testing.T) {
	// Spec: every Retry-After sent by the gateway is capped by the server; a
	// Cloudflare 502 page's 60 s used to pass through unchanged (staging BUD-1).
	withHandlerResilience(t, nil)
	c, w := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseHeaders: http.Header{"Retry-After": []string{"60"}},
		ResponseBody:    []byte(`<html>502 Bad gateway</html>`),
	}, false)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Equal(t, "30", w.Header().Get("Retry-After"))

	// No upstream value: a 5xx still tells the client when to come back.
	c, w = newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}, false)
	ra, err := strconv.Atoi(w.Header().Get("Retry-After"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, ra, 2)
	require.LessOrEqual(t, ra, 30)

	// Switch off: the upstream value passes through as in 0.3.0.
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	c, w = newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseHeaders: http.Header{"Retry-After": []string{"60"}},
	}, false)
	require.Equal(t, "60", w.Header().Get("Retry-After"))
}

// turnCapContext is a request of conversation (key 9301, group 28) after
// selection noted it.
func turnCapContext(t *testing.T, endpoint string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var c *gin.Context
	var w *httptest.ResponseRecorder
	if endpoint == "" {
		c, w = newResilienceTestContext(t)
	} else {
		c, w = newGinContextForEndpoint(t, endpoint)
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
	}
	st := resilience.StateFrom(c.Request.Context())
	st.SetAPIKey(9301)
	st.NoteTurn(28, "gpt-6.1-sol", "sess")
	return c, w
}

func resetTurnCap(t *testing.T) {
	t.Helper()
	key := resilience.TurnKey(9301, 28, "gpt-6.1-sol", "sess")
	resilience.ProcessTurnCap().Reset(key)
	t.Cleanup(func() { resilience.ProcessTurnCap().Reset(key) })
}

func TestTurnCapEndsCodexTurn(t *testing.T) {
	// Spec: Codex follows every Retry-After and retried one failed turn 30
	// times on staging; the fifth exhausted failure in a row is final: 503
	// server_is_overloaded, no Retry-After, x-should-retry: false.
	withHandlerResilience(t, nil)
	resetTurnCap(t)
	for i := 1; i <= 4; i++ {
		c, w := turnCapContext(t, "")
		status := applyExhaustedRetryHint(c, nil, http.StatusBadGateway)
		(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "upstream_error", "Upstream service temporarily unavailable", false)
		require.Equal(t, http.StatusBadGateway, w.Code, "failure %d", i)
		require.Equal(t, "30", w.Header().Get("Retry-After"))
		require.Empty(t, gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	}
	c, w := turnCapContext(t, "")
	c.Header("Retry-After", "60") // copied from the upstream page
	status := applyExhaustedRetryHint(c, nil, http.StatusBadGateway)
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "upstream_error", "Upstream service temporarily unavailable", false)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Equal(t, "false", w.Header().Get("x-should-retry"))
	require.Equal(t, "server_is_overloaded", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	require.NotContains(t, gjson.GetBytes(w.Body.Bytes(), "error.message").String(), "try again in")

	// A 429 stays 429 without Retry-After, which Codex does not retry.
	c, w = turnCapContext(t, "")
	status = applyExhaustedRetryHint(c, nil, http.StatusTooManyRequests)
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "rate_limit_error", "Upstream rate limit exceeded, please retry later", false)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Equal(t, "false", w.Header().Get("x-should-retry"))
}

func TestTurnCapNoAccountAndCommittedStream(t *testing.T) {
	// Spec: no-account 503s count toward the same streak; a failure written
	// into a committed Responses stream ends the turn the same way.
	withHandlerResilience(t, nil)
	resetTurnCap(t)
	groupID := int64(28)
	for i := 1; i <= 4; i++ {
		c, w := turnCapContext(t, "")
		cls := classifyNoAccountErrorFromGin(c, fakeRetryAdvisor{wait: 12 * time.Second}, &service.APIKey{GroupID: &groupID}, "gpt-6.1-sol", "gpt-6.1-sol", service.PlatformOpenAI)
		(&GatewayHandler{}).handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, false)
		require.Equal(t, "12", w.Header().Get("Retry-After"), "failure %d", i)
	}
	c, w := turnCapContext(t, EndpointResponses)
	_, _ = c.Writer.WriteString(":\n\n")
	status := applyExhaustedRetryHint(c, nil, http.StatusBadGateway)
	(&GatewayHandler{}).handleStreamingAwareError(c, status, "upstream_error", "Upstream service temporarily unavailable", true)
	_, errObj := parseResponsesFailedSSE(t, strings.TrimPrefix(w.Body.String(), ":\n\n"))
	require.Equal(t, "server_is_overloaded", errObj["code"])
	require.Equal(t, "Upstream service temporarily unavailable", errObj["message"])
	require.True(t, resilience.StateFrom(c.Request.Context()).TurnFailed(), "a 200 carrying the failure must not reset the streak")

	// Without a noted conversation (failure before selection) nothing counts.
	c, w = newResilienceTestContext(t)
	for i := 0; i < 6; i++ {
		require.False(t, resilienceCapTurn(c))
	}
	require.Empty(t, w.Header().Get("x-should-retry"))
}

func TestTurnCapOffKeepsRetryAfter(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.TurnCap.Enabled = false })
	resetTurnCap(t)
	for i := 1; i <= 6; i++ {
		c, w := turnCapContext(t, "")
		status := applyExhaustedRetryHint(c, nil, http.StatusBadGateway)
		(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "upstream_error", "Upstream service temporarily unavailable", false)
		require.Equal(t, http.StatusBadGateway, w.Code)
		require.Equal(t, "30", w.Header().Get("Retry-After"))
	}
}

// passthroughRuleRepo serves a fixed set of error passthrough rules.
type passthroughRuleRepo struct {
	service.ErrorPassthroughRepository
	rules []*model.ErrorPassthroughRule
}

func (r *passthroughRuleRepo) List(context.Context) ([]*model.ErrorPassthroughRule, error) {
	return r.rules, nil
}

func TestPassthroughRuleBeatsHiddenBalanceOnOpenAIRoutes(t *testing.T) {
	// As on the gateway routes, an administrator's passthrough rule for a
	// balance error is honoured instead of the generic 503.
	withHandlerResilience(t, nil)
	code, msg := http.StatusPaymentRequired, "Top up the relay"
	rules := &passthroughRuleRepo{rules: []*model.ErrorPassthroughRule{{
		ID: 1, Name: "quota", Enabled: true, Priority: 1, ErrorCodes: []int{429}, Keywords: []string{"insufficient_quota"},
		MatchMode: model.MatchModeAll, Platforms: []string{"openai"}, ResponseCode: &code, CustomMessage: &msg,
	}}}
	h := &OpenAIGatewayHandler{errorPassthroughService: service.NewErrorPassthroughService(rules, nil)}
	failover := &service.UpstreamFailoverError{
		StatusCode:   http.StatusTooManyRequests,
		ResponseBody: []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"You exceeded your current quota"}}`),
	}
	c, w := newResilienceTestContext(t)
	h.handleFailoverExhausted(c, failover, false)
	require.Equal(t, http.StatusPaymentRequired, w.Code)
	require.Contains(t, w.Body.String(), "Top up the relay")
	require.Empty(t, w.Header().Get("Retry-After"), "the hidden-balance hints were not applied")

	// Without a rule the balance stays hidden.
	c, w = newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failover, false)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "quota")
}
