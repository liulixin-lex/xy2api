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
	"github.com/tidwall/gjson"
)

const zhipuPreflightMessage = "Request rejected by preflight safety checks"

// contentRefusal is the failover a pool-mode Zhipu account produced for one
// user's content in production: 403, retryable on the same account.
func contentRefusal() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:             http.StatusForbidden,
		ResponseBody:           []byte(`{"code":"request_check_blocked","message":"` + zhipuPreflightMessage + `"}`),
		RetryableOnSameAccount: true,
		SameAccountRetryDelay:  time.Millisecond,
	}
}

func TestContentRefusalEndsFailoverWithoutRetry(t *testing.T) {
	// R4: 0.3.0 retried the pool-mode account three times (about 6 s each)
	// and then switched; every attempt is refused the same way.
	withHandlerResilience(t, nil)
	for name, ctx := range map[string]context.Context{
		"budget":    budgetCtx(t, resilience.KindText, 75),
		"no budget": context.Background(),
	} {
		fs := NewFailoverState(10, false)
		sched := &mockTempUnscheduler{}
		require.Equal(t, FailoverExhausted, fs.HandleFailoverError(ctx, sched, 399, service.PlatformZhipu, 3, contentRefusal()), name)
		require.Zero(t, fs.SameAccountRetryCount[399], name)
		require.Zero(t, fs.SwitchCount, name)
		require.Empty(t, fs.FailedAccountIDs, name)
		require.NotNil(t, fs.LastFailoverErr, name)
		require.True(t, resilienceAttemptFailed(ctx, 399, contentRefusal()), name)
	}

	// An ordinary AUTH 403 switches accounts without ending the request.
	fs := NewFailoverState(10, false)
	other := contentRefusal()
	other.ResponseBody = []byte(`{"error":{"message":"forbidden"}}`)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, 399, service.PlatformZhipu, 3, other))
	require.Zero(t, fs.SameAccountRetryCount[399])
	require.Equal(t, 1, fs.SwitchCount)

	// Switch off: 0.3.0 retries the same account.
	withHandlerResilience(t, func(s *resilience.Settings) { s.Enabled = false })
	fs = NewFailoverState(10, false)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, 399, service.PlatformZhipu, 3, contentRefusal()))
	require.Equal(t, 1, fs.SameAccountRetryCount[399])
}

func TestContentRefusalAnswersClientWith400(t *testing.T) {
	withHandlerResilience(t, nil)

	// OpenAI routes (Chat Completions, Responses, images...).
	c, w := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, contentRefusal(), false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, "content_policy_violation", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	require.Equal(t, zhipuPreflightMessage, gjson.GetBytes(w.Body.Bytes(), "error.message").String())
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Equal(t, http.StatusForbidden, c.GetInt(service.OpsUpstreamStatusCodeKey), "administrators see the upstream 403")

	// Anthropic Messages on the OpenAI gateway.
	c, w = newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, contentRefusal(), false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "error", gjson.GetBytes(w.Body.Bytes(), "type").String())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, zhipuPreflightMessage, gjson.GetBytes(w.Body.Bytes(), "error.message").String())

	// Anthropic gateway.
	c, w = newResilienceTestContext(t)
	(&GatewayHandler{}).handleFailoverExhausted(c, contentRefusal(), service.PlatformAnthropic, false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, zhipuPreflightMessage, gjson.GetBytes(w.Body.Bytes(), "error.message").String())

	// Gateway Chat Completions and Responses.
	c, w = newResilienceTestContext(t)
	(&GatewayHandler{}).handleCCFailoverExhausted(c, contentRefusal(), false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "content_policy_violation", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	c, w = newResilienceTestContext(t)
	(&GatewayHandler{}).handleResponsesFailoverExhausted(c, contentRefusal(), false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "content_policy_violation", gjson.GetBytes(w.Body.Bytes(), "error.code").String())

	// Gemini.
	c, w = newResilienceTestContext(t)
	(&GatewayHandler{}).handleGeminiFailoverExhausted(c, contentRefusal())
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, zhipuPreflightMessage, gjson.GetBytes(w.Body.Bytes(), "error.message").String())

	// A committed stream gets the refusal as its error frame.
	c, w = newResilienceTestContext(t)
	_, _ = c.Writer.WriteString("event: ping\ndata: {}\n\n")
	(&GatewayHandler{}).handleFailoverExhausted(c, contentRefusal(), service.PlatformAnthropic, true)
	require.Contains(t, w.Body.String(), `"type":"invalid_request_error","code":"content_policy_violation"`)
}

func TestContentRefusalMessageIsScrubbed(t *testing.T) {
	withHandlerResilience(t, nil)
	refusal := contentRefusal()
	refusal.ResponseBody = []byte(`{"error":{"code":"content_policy_violation","message":"blocked by https://relay.example.invalid/v1/chat?key=sk-secret content policy"}}`)
	c, w := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, refusal, false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	msg := gjson.GetBytes(w.Body.Bytes(), "error.message").String()
	require.NotContains(t, msg, "sk-secret")
	require.NotContains(t, msg, "relay.example.invalid")
	require.Contains(t, msg, "content policy")

	// No upstream text: a generic refusal.
	refusal.ResponseBody = []byte(`{"error":{"code":"content_filter"}}`)
	c, w = newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, refusal, false)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, contentRejectionFallbackMessage, gjson.GetBytes(w.Body.Bytes(), "error.message").String())
}

func TestOtherForbiddenAndSwitchOffKeepLegacyAnswer(t *testing.T) {
	withHandlerResilience(t, nil)
	forbidden := &service.UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(`{"error":{"message":"forbidden"}}`)}
	c, w := newResilienceTestContext(t)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, forbidden, false)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "Upstream access forbidden")

	for name, mutate := range map[string]func(*resilience.Settings){
		"master off": func(s *resilience.Settings) { s.Enabled = false },
		"errors off": func(s *resilience.Settings) { s.Errors.Enabled = false },
	} {
		withHandlerResilience(t, mutate)
		c, w = newResilienceTestContext(t)
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, contentRefusal(), false)
		require.Equal(t, http.StatusBadGateway, w.Code, name)
		require.Contains(t, w.Body.String(), "Upstream access forbidden", name)
		c, w = newResilienceTestContext(t)
		(&GatewayHandler{}).handleFailoverExhausted(c, contentRefusal(), service.PlatformAnthropic, false)
		require.Equal(t, http.StatusBadGateway, w.Code, name)
	}
}
