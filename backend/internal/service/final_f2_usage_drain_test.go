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
		"decode_error": `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":123}` + "\n\n",
		"reasoning_conflict": `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}` + "\n\n" +
			`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"abc"}` + "\n\n" +
			`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"xyz"}` + "\n\n",
	}
	for _, target := range []string{"chat", "messages"} {
		for name, bad := range cases {
			phases := []string{"pre_output", "pre_output_usage", "after_output"}
			if name == "reasoning_conflict" {
				// The reasoning delta itself commits output before the conflict.
				phases = []string{"reasoning_output"}
			}
			for _, phase := range phases {
				t.Run(target+"/"+name+"/"+phase, func(t *testing.T) {
					prefix := `data: {"type":"response.created","response":{"id":"resp_c1","output":[]}}` + "\n\n"
					if phase == "pre_output_usage" {
						prefix = `data: {"type":"response.created","response":{"id":"resp_c1","output":[],"usage":{"input_tokens":13,"output_tokens":0}}}` + "\n\n"
					}
					if phase == "after_output" {
						prefix += `data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"hello"}` + "\n\n"
					}
					wire := prefix + bad + `data: {"type":"response.completed","response":{"id":"resp_c1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}}` + "\n\n"
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
					require.Error(t, err)
					body := rec.Body.String()
					require.NotContains(t, body, `"finish_reason":"stop"`)
					require.NotContains(t, body, "event: message_stop")
					require.Equal(t, 1, strings.Count(body, `"error":`))
					if phase == "pre_output" {
						require.Equal(t, http.StatusBadGateway, rec.Code)
						require.Nil(t, result, "return immediately without reading the later terminal usage")
						return
					}
					require.NotNil(t, result)
					require.True(t, result.UsagePresent)
					require.True(t, result.PartialFailure)
					require.Equal(t, 13, result.Usage.InputTokens)
					if phase == "pre_output_usage" {
						require.Equal(t, http.StatusBadGateway, rec.Code)
						require.Zero(t, result.Usage.OutputTokens, "retain only usage observed before the error")
						require.False(t, result.TerminalSeen)
					} else {
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, 5, result.Usage.OutputTokens)
					}
				})
			}
		}
	}
}

func TestFinalF2ChatFallbackDrainsUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"responses", "messages"} {
		for _, phase := range []string{"pre_output", "pre_output_usage", "after_output"} {
			t.Run(target+"/"+phase, func(t *testing.T) {
				wire := ""
				if phase == "after_output" {
					wire = "data: " + `{"id":"chat_f2","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n"
				}
				bad := `{"id":"chat_f2","choices":[{"index":0,"delta":{"content":123}}]}`
				if phase == "pre_output_usage" {
					bad = `{"id":"chat_f2","choices":[{"index":0,"delta":{"content":123}}],"usage":{"prompt_tokens":13,"completion_tokens":0}}`
				}
				wire += "data: " + bad + "\n\n" +
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
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`))
				require.NotContains(t, rec.Body.String(), "event: message_stop")
				require.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
				if phase == "pre_output" {
					require.Equal(t, http.StatusBadGateway, rec.Code)
					require.Nil(t, result, "return immediately without reading later usage")
					return
				}
				require.NotNil(t, result)
				require.Equal(t, 13, result.Usage.InputTokens)
				require.True(t, result.UsagePresent)
				require.True(t, result.PartialFailure)
				if phase == "pre_output_usage" {
					require.Equal(t, http.StatusBadGateway, rec.Code)
					require.Zero(t, result.Usage.OutputTokens)
				} else {
					require.Equal(t, 5, result.Usage.OutputTokens)
				}
			})
		}
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
	for _, phase := range []string{"pre_output", "pre_output_usage", "after_output"} {
		t.Run(phase, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/responses", "")
			prefix := `{"type":"message_start","message":{"id":"msg_f2","role":"assistant","content":[]}}`
			if phase == "pre_output_usage" {
				prefix = `{"type":"message_start","message":{"id":"msg_f2","role":"assistant","content":[],"usage":{"input_tokens":13}}}`
			} else if phase == "after_output" {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString("data: {}\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
			}
			wire := "event: message_start\ndata: " + prefix + "\n\n" +
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
			require.Equal(t, phase != "pre_output", outcome.UsagePresent)
			require.Equal(t, phase == "pre_output", outcome.noResult)
			require.Equal(t, phase == "after_output", outcome.TerminalSeen)
			require.Equal(t, phase == "after_output", outcome.TerminalValid)
			require.Equal(t, 1, calls, "do not re-enter the failed converter")
			require.False(t, finalized)
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`))
		})
	}
}
