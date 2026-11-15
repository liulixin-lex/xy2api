//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestM7MessagesKeepsUnknownActualEffort(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			f := newHandlerCompatFixture(t)
			f.group.Platform = platform
			f.account.Platform = platform
			body := `{"model":"glm-4.7","max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hello"}],"stream":true}`
			f.response = func(uint64) string {
				return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"glm-4.7\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			if platform == service.PlatformGemini {
				body = `{"model":"gemini-2.5-flash","max_tokens":4096,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hello"}],"stream":true}`
				f.response = func(uint64) string {
					return "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":2,\"totalTokenCount\":12}}\n\n"
				}
				f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(f.accounts, &fakeGroupRepo{group: f.group}, f.store, service.NewSchedulerSnapshotService(f.snapshot, nil, nil, nil, nil), nil, nil, &handlerCompatHTTP{client: http.DefaultClient}, nil, f.handler.cfg)
			}
			rec := httptest.NewRecorder()
			f.call("messages", body, rec)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "hello")
			require.Len(t, f.usage.logs, 1)
			require.Nil(t, f.usage.logs[0].ReasoningEffort, "the handler must preserve an unknown actual value")
			if platform == service.PlatformGemini {
				require.NotNil(t, f.usage.logs[0].RequestedReasoningEffort)
				require.Equal(t, "high", *f.usage.logs[0].RequestedReasoningEffort)
			}
		})
	}
}

func TestM7PostCommitProtocolErrors(t *testing.T) {
	for _, protocol := range []string{"chat/completions", "messages", "responses"} {
		for _, handlerKind := range []string{"gateway", "openai"} {
			t.Run(handlerKind+"/"+protocol, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
				c.Header("Content-Type", "text/event-stream")
				c.Writer.WriteString(": heartbeat\n\n")
				c.Writer.Flush()
				switch handlerKind {
				case "gateway":
					h := &GatewayHandler{}
					h.handleStreamingAwareErrorWithCode(c, 502, "api_error", "fixture", "failure", false)
					h.handleStreamingAwareErrorWithCode(c, 502, "api_error", "fixture", "again", true)
				case "openai":
					h := &OpenAIGatewayHandler{}
					if protocol == "messages" {
						h.anthropicErrorResponse(c, 502, "api_error", "failure")
						h.ensureAnthropicErrorResponse(c, true)
					} else {
						h.handleStreamingAwareErrorWithCode(c, 502, "api_error", "fixture", "failure", false, false)
						h.handleStreamingAwareErrorWithCode(c, 502, "api_error", "fixture", "again", true, false)
					}
				}
				require.Equal(t, 200, rec.Code)
				require.True(t, service.ProtocolTerminalClaimed(c))
				require.NotContains(t, rec.Body.String(), "again")
				switch protocol {
				case "chat/completions":
					require.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"))
					require.NotContains(t, rec.Body.String(), "event: error")
				case "messages":
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
					require.NotContains(t, rec.Body.String(), "[DONE]")
				case "responses":
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"))
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.created"))
					require.NotContains(t, rec.Body.String(), "[DONE]")
				}
			})
		}
	}
}

func TestM7ContinuationExhaustionKeepsStructuredError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		c, rec := newGinContextForEndpoint(t, EndpointResponses)
		if stream {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			_, err := c.Writer.WriteString(": heartbeat\n\n")
			require.NoError(t, err)
		}
		expected := service.NewHTTPContinuationUnsupportedError()
		h := &OpenAIGatewayHandler{}
		h.handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: 502, Reason: service.OpenAIHTTPContinuationUnsupportedReason}, stream)
		before := rec.Body.String()
		h.handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: 502, Reason: service.OpenAIHTTPContinuationUnsupportedReason}, stream)
		require.Equal(t, before, rec.Body.String())
		if stream {
			events := parseSSEEvents(before)
			require.Len(t, events, 2)
			require.Equal(t, "response.failed", events[1].name)
			require.Contains(t, events[1].data, expected.Code)
			require.Contains(t, events[1].data, expected.Param)
			require.Contains(t, events[1].data, expected.Message)
		} else {
			require.Equal(t, 400, rec.Code)
			require.Contains(t, before, expected.Code)
			require.Contains(t, before, expected.Param)
			require.Contains(t, before, expected.Message)
		}
	}
}
