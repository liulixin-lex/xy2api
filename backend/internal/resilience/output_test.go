package resilience

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFirstoutFrameMatrix(t *testing.T) {
	for _, field := range []struct {
		event, doneField string
		answer           bool
	}{
		{"output_text", "text", true}, {"refusal", "refusal", false},
		{"reasoning_text", "text", false}, {"reasoning_summary_text", "text", false},
		{"function_call_arguments", "arguments", false}, {"custom_tool_call_input", "input", false},
	} {
		for _, value := range []string{"", " ", "\n", "\u200b", "x"} {
			for _, suffix := range []string{"delta", "done"} {
				key := field.doneField
				if suffix == "delta" {
					key = suffix
				}
				event := "response." + field.event + "." + suffix
				raw, err := json.Marshal(map[string]string{"type": event, key: value, "vendor_note": "not output"})
				require.NoError(t, err)
				t.Run(fmt.Sprintf("%s/%q", event, value), func(t *testing.T) {
					got := ClassifyOutput("responses", event, raw)
					require.Equal(t, OutputClass{Known: true, Valid: value == "x", Answer: field.answer && value == "x"}, got)
				})
			}
		}
	}
	cases := []struct {
		name, protocol, payload string
		known, valid, answer    bool
	}{
		{"role", "chat", `{"choices":[{"delta":{"role":"assistant"}}]}`, true, false, false},
		{"tool id", "chat", `{"choices":[{"delta":{"tool_calls":[{"id":"synthetic","function":{"name":"","arguments":""}}]}}]}`, true, false, false},
		{"tool name", "chat", `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"f"}}]}}]}`, true, true, false},
		{"tool args", "chat", `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{"}}]}}]}`, true, true, false},
		{"usage", "chat", `{"choices":[],"usage":{"output_tokens":1}}`, true, false, false},
		{"finish", "chat", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, true, false, false},
		{"empty tools", "chat", `{"choices":[{"delta":{"tool_calls":[]}}]}`, true, false, false},
		{"signature", "messages", `{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"opaque"}}`, true, false, false},
		{"empty thinking", "messages", `{"type":"content_block_start","content_block":{"type":"thinking","thinking":""}}`, true, false, false},
		{"redacted", "messages", `{"type":"content_block_start","content_block":{"type":"redacted_thinking","data":"opaque"}}`, true, true, false},
		{"tool start", "messages", `{"type":"content_block_start","content_block":{"type":"tool_use","name":"f","input":{}}}`, true, true, false},
		{"empty tool start", "messages", `{"type":"content_block_start","content_block":{"type":"tool_use","id":"id","input":{}}}`, true, false, false},
		{"block stop", "messages", `{"type":"content_block_stop","index":0}`, true, false, false},
		{"message usage", "messages", `{"type":"message_delta","usage":{"output_tokens":2}}`, true, false, false},
		{"empty reasoning item", "responses", `{"type":"response.output_item.done","item":{"type":"reasoning","id":"id","summary":[{"type":"summary_text","text":""}]}}`, true, false, false},
		{"encrypted reasoning", "responses", `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"opaque"}}`, true, true, false},
		{"tool name item", "responses", `{"type":"response.output_item.added","item":{"type":"function_call","name":"f","arguments":""}}`, true, true, false},
		{"tool id item", "responses", `{"type":"response.output_item.done","item":{"type":"function_call","id":"id"}}`, true, false, false},
		{"empty summary part", "responses", `{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text","text":""}}`, true, false, false},
		{"empty completed", "responses", `{"type":"response.completed","response":{"output":[]}}`, true, false, false},
		{"completed answer", "responses", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`, true, true, true},
		{"unknown", "responses", `{"type":"response.vendor.delta","delta":"x"}`, false, false, false},
		{"unknown item", "responses", `{"type":"response.output_item.added","item":{"type":"vendor","text":"x"}}`, false, false, false},
		{"malformed", "responses", `{"type":"response.output_text.delta","delta":`, false, false, false},
		{"numeric text", "responses", `{"type":"response.output_text.delta","delta":1}`, true, false, false},
	}
	for _, event := range []string{"response.created", "response.queued", "response.in_progress", "response.web_search_call.in_progress", "response.file_search_call.searching", "response.code_interpreter_call.interpreting", "response.mcp_list_tools.in_progress", "response.output_text.annotation.added", "ping", "keepalive", "codex.rate_limits"} {
		cases = append(cases, struct {
			name, protocol, payload string
			known, valid, answer    bool
		}{event, "responses", fmt.Sprintf(`{"type":%q}`, event), true, false, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, OutputClass{Known: tc.known, Valid: tc.valid, Answer: tc.answer}, ClassifyOutput(tc.protocol, "", []byte(tc.payload)))
			if tc.protocol == "responses" {
				require.Equal(t, ClassifyOutput("responses", "", []byte(tc.payload)), ClassifyOutput("websocket", "", []byte(tc.payload)))
			}
		})
	}
	for _, value := range []string{"", " ", "\n", "\u200b", "x"} {
		for _, field := range []string{"content", "refusal", "reasoning", "reasoning_content"} {
			raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{field: value}}}})
			require.Equal(t, OutputClass{Known: true, Valid: value == "x", Answer: field == "content" && value == "x"}, ClassifyOutput("chat", "", raw))
		}
		for _, pair := range [][2]string{{"text_delta", "text"}, {"thinking_delta", "thinking"}, {"input_json_delta", "partial_json"}} {
			raw, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": pair[0], pair[1]: value}})
			require.Equal(t, OutputClass{Known: true, Valid: value == "x", Answer: pair[1] == "text" && value == "x"}, ClassifyOutput("messages", "", raw))
		}
	}
}

func TestFirstoutRequestClockAndDispatchPhases(t *testing.T) {
	start := time.Now().Add(-time.Second)
	state := NewRequestState()
	state.SetBudget(NewBudget(context.Background(), Current(), KindText, start))
	state.InitDelivery(context.Background(), start.Add(7*time.Millisecond), nil)
	state.MarkBodyReceived(start.Add(100*time.Millisecond), "test", 100)
	state.BeginDispatchPhase("request_validation_ms", start.Add(110*time.Millisecond))
	state.BeginDispatchPhase("account_selection_ms", start.Add(150*time.Millisecond))
	state.NoteOpenAIDispatch(start.Add(180*time.Millisecond), "test")
	state.BeginDispatchPhase("ignored_retry_ms", start.Add(190*time.Millisecond))
	state.NoteSemanticOutput(start.Add(400 * time.Millisecond))
	timing := state.Timing()
	require.EqualValues(t, 100, *timing.BodyEOFMs)
	require.EqualValues(t, 400, *state.EndToEndFirstOutputMs())
	require.EqualValues(t, 300, *state.EndToEndFirstOutputMs()-*timing.BodyEOFMs)
	require.JSONEq(t, `{"body_decode_ms":10,"request_validation_ms":40,"account_selection_ms":30}`, string(timing.DispatchPhases))
	require.Equal(t, string(timing.DispatchPhases), string(state.Timing().DispatchPhases))
}

func TestFirstoutManagedToolsAndEventIdentity(t *testing.T) {
	for _, kind := range []string{"web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call", "local_shell_call", "shell_call", "apply_patch_call", "computer_call"} {
		for _, phase := range []string{"added", "done"} {
			raw := []byte(fmt.Sprintf(`{"type":"response.output_item.%s","item":{"type":%q}}`, phase, kind))
			require.Equal(t, OutputClass{Known: true, Valid: true}, ClassifyOutput("responses", "", raw))
		}
	}
	for _, tc := range []struct{ event, field string }{
		{"response.code_interpreter_call_code.delta", "delta"}, {"response.code_interpreter_call_code.done", "code"},
		{"response.mcp_call_arguments.delta", "delta"}, {"response.image_generation_call.partial_image", "partial_image_b64"},
		{"response.image_generation_call.completed", "result"},
	} {
		for _, v := range []string{"", " \u200b", "x"} {
			raw, _ := json.Marshal(map[string]string{"type": tc.event, tc.field: v})
			require.Equal(t, OutputClass{Known: true, Valid: v == "x"}, ClassifyOutput("responses", tc.event, raw))
		}
	}
	for _, raw := range []string{
		`{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":"\u200b"}}`,
		`{"type":"response.output_item.added","item":{"type":"message","content":[]}}`,
	} {
		require.Equal(t, OutputClass{Known: true}, ClassifyOutput("responses", "", []byte(raw)))
	}
	require.Equal(t, OutputClass{Known: true, Valid: true, Answer: true}, ClassifyOutput("responses", "", []byte(`{"type":"response.incomplete","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}}`)))
	require.Equal(t, OutputClass{Known: true, Valid: true}, ClassifyOutput("responses", "", []byte(`{"type":"response.web_search_call.completed","action":{"type":"search","query":"fixture"}}`)))
	require.Equal(t, OutputClass{}, ClassifyOutput("responses", "response.created", []byte(`{"type":"response.output_text.delta","delta":"x"}`)))
	require.Equal(t, OutputClass{Known: true}, ClassifyOutput("responses", "", []byte(`[DONE]`)))
}

func TestFirstoutUndispatchedTimingDoesNotIncludeGeneration(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	state := NewRequestState()
	state.InitDelivery(context.Background(), start, nil)
	state.MarkBodyReceived(start.Add(time.Millisecond), "test", 123)
	state.BeginDispatchPhase("routing_validation_ms", start.Add(2*time.Millisecond))
	before := state.timing.phaseStarted
	require.Empty(t, state.Timing().DispatchPhases)
	require.Empty(t, state.Timing().DispatchPhases)
	require.Equal(t, before, state.timing.phaseStarted, "Timing must not advance an unfinished phase")
	state.FinishRejectedDispatch(start.Add(9 * time.Millisecond))
	require.JSONEq(t, `{"body_decode_ms":1,"routing_validation_ms":7}`, string(state.Timing().DispatchPhases))
	state.FinishRejectedDispatch(start.Add(time.Minute))
	require.JSONEq(t, `{"body_decode_ms":1,"routing_validation_ms":7}`, string(state.Timing().DispatchPhases))
	sent := NewRequestState()
	sent.InitDelivery(context.Background(), start, nil)
	sent.MarkBodyReceived(start.Add(time.Millisecond), "test", 123)
	sent.EnableGatewayRequest(start)
	sent.NoteGatewaySend(123)
	sent.FinishRejectedDispatch(start.Add(time.Minute))
	require.Empty(t, sent.Timing().DispatchPhases, "uninstrumented provider sends must not publish generation as dispatch time")
}

func TestFirstoutMCPListAndDuplicateKeysAreUnknown(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.output_item.added","item":{"type":"mcp_list_tools","tools":[{"name":"fixture"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"mcp_list_tools","tools":[]}}`,
		`{"type":"response.output_text.delta","delta":" ","delta":"answer"}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":" ","text":"answer"}]}}`,
		`{"choices":[{"delta":{"content":" ","content":"answer"}}]}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":" ","text":"answer"}}`,
	} {
		require.Equal(t, OutputClass{}, ClassifyOutput("", "", []byte(payload)), payload)
	}
	// Repeated names in distinct objects are not duplicate keys.
	require.Equal(t, OutputClass{Known: true, Valid: true, Answer: true}, ClassifyOutput("", "", []byte(`{"choices":[{"delta":{"content":"a"}},{"delta":{"content":"b"}}]}`)))
}

func TestFirstoutNestedUnknownIsNotEmpty(t *testing.T) {
	for _, fields := range []string{
		`"type":"message","role":"assistant","content":[{"type":"vendor_text","text":"answer"}]`,
		`"type":"message","content":[{"type":"output_text","text":"answer"},{"type":"vendor_text"}]`,
		`"type":"message","content":[{"type":"vendor_text"},{"type":"output_text","text":"answer"}]`,
		`"type":"reasoning","summary":[{"type":"vendor_summary","text":"answer"}]`,
		`"type":"reasoning","content":[{"type":"vendor_thought"}],"encrypted_content":"opaque"`,
	} {
		raw := []byte(`{"type":"response.output_item.added","item":{` + fields + `}}`)
		require.Equal(t, OutputClass{}, ClassifyOutput("responses", "", raw), string(raw))
		terminal := []byte(`{"type":"response.completed","response":{"output":[{` + fields + `}]}}`)
		require.Equal(t, OutputClass{}, ClassifyOutput("responses", "", terminal), string(terminal))
	}
}

func TestFirstoutMCPRequiresNameOrArguments(t *testing.T) {
	for _, kind := range []string{"mcp_call", "mcp_approval_request"} {
		for _, phase := range []string{"added", "done"} {
			for _, fields := range []string{``, `,"name":" ","arguments":""`, `,"name":"\u034f\ufe0f","arguments":"\u200b"`, `,"name":"tool"`, `,"arguments":"{}"`} {
				raw := []byte(fmt.Sprintf(`{"type":"response.output_item.%s","item":{"type":%q,"id":"only-id"%s}}`, phase, kind, fields))
				valid := fields == `,"name":"tool"` || fields == `,"arguments":"{}"`
				require.Equal(t, OutputClass{Known: true, Valid: valid}, ClassifyOutput("responses", "", raw), string(raw))
			}
		}
	}
}

func TestFirstoutDefaultIgnorableCharacters(t *testing.T) {
	for _, invisible := range []string{"\u034f", "\ufe0f", "\U000e0100", "\u115f", "\u3164", "\u17b4", "\u034f\ufe0f"} {
		for _, visible := range []string{"", "a", "字", "❤"} {
			raw, err := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": visible + invisible})
			require.NoError(t, err)
			require.Equal(t, OutputClass{Known: true, Valid: visible != "", Answer: visible != ""}, ClassifyOutput("responses", "", raw), string(raw))
		}
	}
}
