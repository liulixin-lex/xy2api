//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
