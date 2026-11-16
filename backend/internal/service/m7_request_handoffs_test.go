//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM7ConversionRequestHandoffs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body, effort string
		messages           bool
	}{
		{"raw nearest", `{"model":"gpt-6.1-sol","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, "low", false},
		{"raw absent", `{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]}`, "", false},
		{"messages nearest", `{"model":"gpt-6.1-sol","output_config":{"effort":"none"},"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`, "low", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := compatStreamContext("/v1/chat/completions", tc.body)
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			c.Request = c.Request.WithContext(ctx)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			var result *OpenAIForwardResult
			var err error
			if tc.messages {
				result, err = svc.forwardAnthropicViaRawChatCompletions(ctx, c, rawChatCompletionsTestAccount(), []byte(tc.body), "")
			} else {
				result, err = svc.forwardAsRawChatCompletions(ctx, c, rawChatCompletionsTestAccount(), []byte(tc.body), "")
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tc.effort, gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
			if tc.effort == "" {
				require.Nil(t, result.ReasoningEffort)
			} else {
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, tc.effort, *result.ReasoningEffort)
			}
		})
	}
}

func TestM7TerraChatToolsRequireResponses(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			body := `{"model":"gpt-6.1-terra","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
			if protocol == "messages" {
				body = `{"model":"gpt-6.1-terra","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":{"type":"object"}}]}`
			}
			if protocol == "responses" {
				body = `{"model":"gpt-6.1-terra","input":"hi","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]}`
			}
			c, rec := compatStreamContext("/v1/"+protocol, body)
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			var err error
			switch protocol {
			case "chat":
				_, err = svc.forwardAsRawChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(body), "")
			case "messages":
				_, err = svc.forwardAnthropicViaRawChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(body), "")
			default:
				_, err = svc.forwardResponsesViaRawChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(body))
			}
			require.Error(t, err)
			require.Equal(t, 400, rec.Code)
			require.Empty(t, upstream.requests)
			require.Contains(t, rec.Body.String(), "gpt-6.1-terra requires Responses")
		})
	}
}
