//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestPreludeReleaseSchemaFromStream(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"native", "passthrough"} {
		t.Run(kind, func(t *testing.T) {
			logs := captureOpenAIObservationLogs(t)
			c, _ := compatStreamContext("/v1/responses", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			svc := compatStreamService(nil, 0, 0, 0)
			account := rawChatCompletionsTestAccount()
			body := keepaliveStallCreated + `data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"private-item-value","encrypted_content":"private-encrypted-value","summary":[{"text":"private-summary-value"}]}}` + "\n\n" + keepaliveStallDelta1
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			if kind == "native" {
				_, _ = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				_, _ = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}
			require.Positive(t, state.DeliveredBytes())
			var failure map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				if entry["msg"] == "resilience.post_output_failure" {
					failure = entry
				}
			}
			require.NotNil(t, failure)
			require.Equal(t, "response.output_item.added", failure["prelude_release_event"])
			require.Equal(t, "reasoning", failure["prelude_release_item_type"])
			require.Equal(t, "encrypted_content,id,summary,type", failure["prelude_release_item_keys"])
			require.Equal(t, true, failure["prelude_release_has_encrypted"])
			for _, value := range []string{"private-item-value", "private-encrypted-value", "private-summary-value"} {
				require.NotContains(t, logs.String(), value)
			}
		})
	}
}

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

func TestPreludeReleaseSchemaKeysBounded(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, limit := range []string{"count", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			logs := captureOpenAIObservationLogs(t)
			item := map[string]any{"type": "reasoning", "encrypted_content": "private-encrypted-value"}
			if limit == "count" {
				for i := range preludeReleaseMaxKeys {
					item["field"+strconv.Itoa(i)] = "private-field-value"
				}
			} else {
				item[strings.Repeat("x", preludeReleaseMaxKeyBytes+1)] = "private-field-value"
			}
			payload, err := json.Marshal(map[string]any{"type": "response.output_item.added", "item": item})
			require.NoError(t, err)
			c, _ := compatStreamContext("/v1/responses", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			body := keepaliveStallCreated + "data: " + string(payload) + "\n\n" + keepaliveStallDelta1
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			_, _ = compatStreamService(nil, 0, 0, 0).handleStreamingResponse(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "model", "model")
			require.Equal(t, "[truncated;keys="+strconv.Itoa(len(item))+"]", state.PreludeTrace().ItemKeys)
			require.Contains(t, logs.String(), `"msg":"resilience.post_output_failure"`)
			require.Contains(t, logs.String(), `"prelude_release_item_keys":"[truncated;keys=`+strconv.Itoa(len(item))+`]"`)
			require.NotContains(t, logs.String(), "private-field-value")
			require.NotContains(t, logs.String(), "private-encrypted-value")
			require.NotContains(t, logs.String(), strings.Repeat("x", preludeReleaseMaxKeyBytes+1))
		})
	}
}
