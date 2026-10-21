//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestPreludeReleaseTraceFromStream(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"native", "passthrough", "chat", "messages"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := compatStreamContext("/v1/responses", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			svc := compatStreamService(nil, 0, 0, 0)
			run := func(body string) {
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				account := rawChatCompletionsTestAccount()
				switch kind {
				case "native":
					_, _ = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				case "passthrough":
					_, _ = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				case "chat":
					_, _ = svc.handleChatStreamingResponse(resp, c, account, "model", "model", "model", time.Now(), 0)
				case "messages":
					_, _ = svc.handleAnthropicStreamingResponse(resp, c, account, "model", "model", "model", time.Now())
				}
			}
			failed := `data: {"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"busy"}}}` + "\n\n"
			run(keepaliveStallCreated + failed)
			require.Empty(t, state.PreludeTrace().ReleaseEvent)
			run(keepaliveStallCreated + keepaliveStallDelta1 + (`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"))
			require.Equal(t, "response.output_text.delta", state.PreludeTrace().ReleaseEvent)
			require.GreaterOrEqual(t, state.PreludeTrace().ReleaseAfterCreatedMS, int64(0))
			require.Positive(t, state.DeliveredBytes())
		})
	}
}
