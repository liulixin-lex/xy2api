//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestFinalF2ConverterErrorDrainsTerminalUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	cases := map[string]string{
		// Certain: M7 typed business-event decode error (delta must be a string).
		"decode_error": `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":123}` + "\n\n",
		// Likely: M2 converter Err "reasoning snapshot conflicts with delivered prefix".
		"reasoning_conflict": `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}` + "\n\n" +
			`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"abc"}` + "\n\n" +
			`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"xyz"}` + "\n\n",
	}
	for _, target := range []string{"chat", "messages"} {
		for name, bad := range cases {
			t.Run(target+"/"+name, func(t *testing.T) {
				wire := `data: {"type":"response.created","response":{"id":"resp_c1","output":[]}}` + "\n\n" +
					bad +
					`data: {"type":"response.completed","response":{"id":"resp_c1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}}` + "\n\n"
				c, rec := compatStreamContext("/v1/"+map[string]string{"chat": "chat/completions", "messages": "messages"}[target], "")
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				account := rawChatCompletionsTestAccount()
				var result *OpenAIForwardResult
				var err error
				if target == "chat" {
					result, err = svc.handleChatStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now(), 1)
				} else {
					result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
				}
				body := rec.Body.String()
				if err == nil {
					t.Fatalf("expected conversion error; body=%s", body)
				}
				if result == nil {
					t.Fatalf("nil result err=%v", err)
				}
				// Terminal-count sanity (focus 3): no success terminal after the error.
				if strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, "event: message_stop") {
					t.Errorf("success terminal written after conversion error: %s", body)
				}
				if strings.Count(body, `"error":`) != 1 {
					t.Errorf("expected one error response: %s", body)
				}
				if !result.UsagePresent || !result.PartialFailure {
					t.Errorf("missing partial usage facts: %+v", result)
				}
				if result.Usage.InputTokens != 13 || result.Usage.OutputTokens != 5 {
					t.Errorf("partial failure lost terminal usage: input=%d output=%d usagePresent=%t err=%v", result.Usage.InputTokens, result.Usage.OutputTokens, result.UsagePresent, err)
				}
			})
		}
	}
}

func TestFinalF2ChatFallbackDrainsUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"responses", "messages"} {
		t.Run(target, func(t *testing.T) {
			wire := "data: " + `{"id":"chat_f2","choices":[{"index":0,"delta":{"content":123}}]}` + "\n\n" +
				"data: " + `{"id":"chat_f2","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":5,"total_tokens":18}}` + "\n\ndata: [DONE]\n\n"
			c, rec := compatStreamContext("/v1/"+target, "")
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			account := rawChatCompletionsTestAccount()
			var result *OpenAIForwardResult
			var err error
			if target == "responses" {
				result, err = svc.streamChatCompletionsAsResponses(c, resp, account, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now())
			} else {
				result, err = svc.streamChatCompletionsAsAnthropic(c, resp, account, "fixture", "fixture", "fixture", nil, nil, time.Now())
			}
			require.Error(t, err)
			require.NotNil(t, result)
			require.Equal(t, 13, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			require.True(t, result.UsagePresent)
			require.True(t, result.PartialFailure)
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`))
			require.NotContains(t, rec.Body.String(), "event: message_stop")
			require.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
		})
	}
}

func TestFinalF2AnthropicConversionDrainsUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	wire := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_f2","role":"assistant","content":[]}}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_f2","name":"lookup","input":{}}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{bad"}}` + "\n\n" +
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":13,"output_tokens":5}}` + "\n\n" + gwMsgStop
	for _, path := range gwConvertPaths() {
		if path.name != "chat" {
			continue
		}
		t.Run(path.name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/"+path.name, "")
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			svc := gwConvertService(0, 0)
			opts := apicompat.ConversionOptions{Bridge: &apicompat.BridgeOptions{Enabled: true}}
			var result *ForwardResult
			var err error
			if path.name == "chat" {
				result, err = svc.handleCCStreamingFromAnthropic(resp, c, gwTestAccount(), "fixture", "fixture", nil, time.Now(), opts)
			} else {
				result, err = svc.handleResponsesStreamingResponse(resp, c, gwTestAccount(), "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{}, opts)
			}
			require.Error(t, err)
			require.NotNil(t, result)
			require.Equal(t, 13, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			require.True(t, result.UsagePresent)
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`))
			require.NotContains(t, rec.Body.String(), `"finish_reason":"stop"`)
			require.NotContains(t, rec.Body.String(), `"finish_reason":"tool_calls"`)
			require.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
		})
	}
}

func TestFinalF2AnthropicDriverDrainsAfterConversionError(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := compatStreamContext("/v1/responses", "")
	wire := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_f2","role":"assistant","content":[]}}` + "\n\n" +
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":5}}` + "\n\n" + gwMsgStop
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
	conversionErr := &apicompat.UpstreamConversionError{Field: "fixture", Reason: "conversion failed", SourceProtocol: "anthropic"}
	calls := 0
	finalized := false
	driver := gwConvertService(0, 0).newGatewayConvertStream(c, resp, gwTestAccount(), gatewayConvertResponses, "fixture", gatewayConvertHooks{
		convert:         func(*apicompat.AnthropicStreamEvent, string, bool) []string { calls++; return nil },
		conversionError: func() error { return conversionErr },
		finalize:        func() []string { finalized = true; return nil },
	})
	outcome := driver.run()
	require.ErrorIs(t, outcome.err, conversionErr)
	require.True(t, outcome.UsagePresent, "the metering frame following the rejected conversion must still be read")
	require.True(t, outcome.TerminalSeen)
	require.True(t, outcome.TerminalValid)
	require.Equal(t, 1, calls, "do not re-enter the failed converter while draining")
	require.False(t, finalized)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`))
}
