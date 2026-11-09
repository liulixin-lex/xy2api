//go:build unit

package service

import (
	"context"
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
			require.NotContains(t, failure, "prelude_release_event_keys")
			for _, value := range []string{"private-item-value", "private-encrypted-value", "private-summary-value"} {
				require.NotContains(t, logs.String(), value)
			}
		})
	}
}

func TestPreludeReleaseEventSchemaFromStream(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"native", "passthrough"} {
		t.Run(kind, func(t *testing.T) {
			logs := captureOpenAIObservationLogs(t)
			c, recorder := compatStreamContext("/v1/responses", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			// Unknown event types stay staged until the semantic delta releases the prefix.
			payload := `{"type":"proxy.ready","sequence_number":987654,"gateway":{"private-child":"private-nested-value"},"items":["private-array-value"],"ready":true,"detail":"private-detail-value","optional":null}`
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n" + keepaliveStallDelta1))}
			svc := compatStreamService(nil, 0, 0, 0)
			account := rawChatCompletionsTestAccount()
			if kind == "native" {
				_, _ = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				_, _ = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}
			require.Positive(t, state.DeliveredBytes())
			require.Contains(t, recorder.Body.String(), payload, "observation must preserve the released event")
			var failure map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				if entry["msg"] == "resilience.post_output_failure" {
					failure = entry
				}
			}
			require.NotNil(t, failure)
			require.Equal(t, "response.output_text.delta", failure["prelude_release_event"])
			require.Equal(t, "delta:string,item_id:string,type:string", failure["prelude_release_event_keys"])
			require.Empty(t, failure["prelude_release_item_keys"])
			for _, value := range []string{"987654", "private-child", "private-nested-value", "private-array-value", "private-detail-value"} {
				require.NotContains(t, logs.String(), value)
			}
		})
	}
}

func TestPreludeReleaseEventSchemaScope(t *testing.T) {
	for _, tc := range []struct {
		name, event, payload, want string
	}{
		{"output_item_added_standard", "response.output_item.added", `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"reasoning"}}`, ""},
		{"output_item_added_extra", "response.output_item.added", `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"reasoning"},"sub2api_placeholder":true}`, "item:object,output_index:number,sequence_number:number,sub2api_placeholder:boolean,type:string"},
		{"output_item_done_standard", "response.output_item.done", `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"reasoning"}}`, ""},
		{"output_item_done_no_sequence", "response.output_item.done", `{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"private"}}`, ""},
		{"output_item_unknown", "response.output_item.custom", `{"type":"response.output_item.custom","item":{"type":"reasoning"}}`, "item:object,type:string"},
		{"output_text_delta", "response.output_text.delta", `{"type":"response.output_text.delta","delta":"private"}`, "delta:string,type:string"},
		{"content_part_added", "response.content_part.added", `{"type":"response.content_part.added","part":{"type":"refusal","refusal":"private"}}`, "part:object,type:string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := resilience.NewRequestState()
			releaseOpenAIPrelude(state, tc.event, []byte(tc.payload))
			require.Equal(t, tc.want, state.PreludeTrace().EventKeys)
			fields := resilience.EventFields(resilience.WithRequestState(context.Background(), state))
			logged := map[string]any{}
			for i := 0; i < len(fields); i += 2 {
				logged[fields[i].(string)] = fields[i+1]
			}
			if tc.want == "" {
				require.NotContains(t, logged, "prelude_release_event_keys")
			} else {
				require.Equal(t, tc.want, logged["prelude_release_event_keys"])
			}
		})
	}
}

func TestPreludeReleaseEventSchemaKeysBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		bytes int
	}{
		{"at_key_limit", preludeReleaseMaxKeys, 0},
		{"over_key_limit", preludeReleaseMaxKeys + 1, 0},
		{"at_byte_limit", 0, preludeReleaseMaxKeyBytes},
		{"over_byte_limit", 0, preludeReleaseMaxKeyBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := map[string]any{}
			for i := range tc.count {
				envelope[strconv.Itoa(i)] = nil
			}
			if tc.bytes != 0 {
				// Include the type annotation in the byte limit; use UTF-8 keys.
				keyBytes := tc.bytes - len(":null")
				envelope[strings.Repeat("界", keyBytes/3)+strings.Repeat("x", keyBytes%3)] = nil
			}
			payload, err := json.Marshal(envelope)
			require.NoError(t, err)
			state := resilience.NewRequestState()
			releaseOpenAIPrelude(state, "proxy.ready", payload)
			got := state.PreludeTrace().EventKeys
			if tc.count > preludeReleaseMaxKeys || tc.bytes > preludeReleaseMaxKeyBytes {
				require.Equal(t, "[truncated;keys="+strconv.Itoa(len(envelope))+"]", got)
			} else {
				require.NotContains(t, got, "truncated")
				if tc.bytes > 0 {
					require.Len(t, got, tc.bytes)
				} else {
					require.Len(t, strings.Split(got, ","), tc.count)
				}
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
