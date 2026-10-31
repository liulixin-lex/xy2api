//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// OpenAI-group native Anthropic routes skip finalize, so they reconcile in
// their own converted limit step.
func TestAnthropicNativeConvertedCacheTTLWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raws := map[string]string{
		"chat":      `{"model":"k3","stream":true,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
		"responses": `{"model":"k3","stream":true,"cache_control":{"type":"ephemeral"},"input":[{"type":"message","role":"user","content":"go"},{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
	}
	for route, raw := range raws {
		t.Run(route, func(t *testing.T) {
			sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"k3\",\"content\":[]}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route, strings.NewReader(raw))
			var err error
			if route == "chat" {
				_, err = svc.forwardChatCompletionsViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), []byte(raw), "")
			} else {
				_, err = svc.forwardResponsesViaNativeAnthropic(context.Background(), c, nativeAnthropicTestAccount(), []byte(raw), "")
			}
			require.NoError(t, err)
			wire := upstream.lastBody
			require.Equal(t, "1h", gjson.GetBytes(wire, "cache_control.ttl").String(), string(wire))
			require.Equal(t, "tool_result", gjson.GetBytes(wire, "messages.2.content.0.type").String())
			require.Equal(t, "1h", gjson.GetBytes(wire, "messages.2.content.0.cache_control.ttl").String())
			require.False(t, anthropicAutomaticTailConflict(wire))
		})
	}
}

// Chat tool markers are for Anthropic conversions only; the OpenAI Responses
// body built from the same Chat request does not gain them.
func TestChatToolCacheMarkersStayOffOpenAIResponsesWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"go"},{"role":"assistant","cache_control":{"type":"ephemeral"},"tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 2, Name: "openai-compatible", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-compatible"}, Extra: map[string]any{"openai_responses_supported": true}}
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.4")
	require.Error(t, err)
	types := []string{}
	for _, item := range gjson.GetBytes(upstream.lastBody, "input").Array() {
		types = append(types, item.Get("type").String())
	}
	require.Equal(t, []string{"message", "function_call", "function_call_output"}, types, string(upstream.lastBody))
	require.NotContains(t, string(upstream.lastBody), "cache_control")
}
