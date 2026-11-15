package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM3ForeignReasoningEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, signature string
		keep            bool
	}{
		{"v1", "ccswitch-openai-reasoning-v1:eyJ0ZXh0IjoiYSJ9", false},
		{"v1 damaged", "ccswitch-openai-reasoning-v1:broken", false},
		{"v1 whitespace", " \tccswitch-openai-reasoning-v1:data\n", false},
		{"empty", " \t", false},
		{"gAAAA", "gAAAA-ciphertext", false},
		{"provider", " provider-signature ", true},
		{"unknown version", "ccswitch-openai-reasoning-v2:data", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := []AnthropicContentBlock{
				{Type: "thinking", Thinking: "thought", Signature: tc.signature},
				{Type: "text", Text: "answer"},
				{Type: "tool_use", ID: "call_a", Name: "f", Input: json.RawMessage(`{"x":1}`)},
			}
			raw, err := json.Marshal(blocks)
			require.NoError(t, err)
			items, err := anthropicAssistantToResponses(raw)
			require.NoError(t, err)
			if tc.keep {
				require.Len(t, items, 3)
				require.Equal(t, "reasoning", items[0].Type)
				require.Equal(t, strings.TrimSpace(tc.signature), items[0].EncryptedContent)
				items = items[1:]
			}
			require.Len(t, items, 2)
			require.Equal(t, "message", items[0].Type)
			require.JSONEq(t, `[{"type":"output_text","text":"answer"}]`, string(items[0].Content))
			require.Equal(t, "function_call", items[1].Type)
			require.Equal(t, "call_a", items[1].CallID)
			require.Equal(t, `{"x":1}`, items[1].Arguments)
		})
	}
}

func TestM3TopLevelCacheControlBoundary(t *testing.T) {
	var req AnthropicRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"test","max_tokens":1024,"cache_control":{"type":"ephemeral","ttl":"1h","scope":"global","future_flag":true},"system":[{"type":"text","text":"instructions","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`), &req))
	before, err := json.Marshal(req)
	require.NoError(t, err)
	responses, err := AnthropicToResponses(&req)
	require.NoError(t, err)
	native, err := responses.NativeWireRequest()
	require.NoError(t, err)
	chat, err := AnthropicToChatCompletionsRequest(&req)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		wire any
	}{
		{"responses", responses}, {"native responses", native}, {"chat", chat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := json.Marshal(tc.wire)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(wire, &fields))
			require.NotContains(t, fields, "cache_control")
			// Existing block markers are not copied into either OpenAI wire.
			require.NotContains(t, string(wire), `"cache_control"`)
		})
	}
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	require.Equal(t, "1h", req.CacheControl.TTL)
	require.Equal(t, "global", req.CacheControl.Scope)
	require.JSONEq(t, `true`, string(req.CacheControl.Extra["future_flag"]))
}

func TestM3ToolConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, fields            string
		strict, notice, invalid bool
	}{
		{name: "default"}, {name: "true", fields: `,"strict":true`, strict: true}, {name: "false", fields: `,"strict":false`},
		{name: "null", fields: `,"allowed_callers":null`}, {name: "direct", fields: `,"allowed_callers":["direct"]`},
		{name: "executor", fields: `,"allowed_callers":["code_execution_20250825"]`, notice: true},
		{name: "mixed", fields: `,"allowed_callers":["direct","executor"]`, notice: true},
		{name: "empty", fields: `,"allowed_callers":[]`, notice: true}, {name: "empty name", fields: `,"allowed_callers":[""]`, notice: true},
		{name: "string", fields: `,"allowed_callers":"direct"`, invalid: true}, {name: "number", fields: `,"allowed_callers":[1]`, invalid: true},
		{name: "null member", fields: `,"allowed_callers":[null]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req AnthropicRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":{"type":"object","properties":{"optional":{"type":"string"}}}`+tc.fields+`}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`), &req))
			var notices []ConversionNotice
			out, err := AnthropicToResponsesWithOptions(&req, ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }})
			if tc.invalid {
				var bad *UnsupportedConversionError
				require.ErrorAs(t, err, &bad)
				require.Equal(t, "tools[0].allowed_callers", bad.Field)
				return
			}
			require.NoError(t, err)
			require.Len(t, out.Tools, 1)
			require.NotNil(t, out.Tools[0].Strict)
			require.Equal(t, tc.strict, *out.Tools[0].Strict)
			require.JSONEq(t, string(req.Tools[0].InputSchema), string(out.Tools[0].Parameters))
			require.False(t, *out.ParallelToolCalls)
			if tc.notice {
				require.Equal(t, []ConversionNotice{{Field: "tools[0].allowed_callers", Action: "omit"}}, notices)
			} else {
				require.Empty(t, notices)
			}
			legacy, err := AnthropicToResponses(&req)
			require.NoError(t, err)
			require.Equal(t, legacy, out)
			zero, err := AnthropicToResponsesWithOptions(&req, ConversionOptions{})
			require.NoError(t, err)
			require.Equal(t, legacy, zero)
		})
	}
}

func m3ToolEvent(kind string, index int, id, args string) *ResponsesStreamEvent {
	return &ResponsesStreamEvent{Type: kind, OutputIndex: index, OutputIndexPresent: true, ItemID: id, Arguments: args, ArgumentsPresent: true}
}

func TestM3ParallelToolQueue(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, insert := range []string{"none", "text", "thinking", "search"} {
			t.Run(fmt.Sprintf("reverse=%v/%s", reverse, insert), func(t *testing.T) {
				st := NewResponsesEventToAnthropicState()
				var events []AnthropicStreamEvent
				feed := func(e *ResponsesStreamEvent) { events = append(events, ResponsesEventToAnthropicEvents(e, st)...) }
				for i, id := range []string{"a", "b"} {
					feed(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: i, OutputIndexPresent: true, Item: &ResponsesOutput{Type: "function_call", ID: "fc_" + id, CallID: "call_" + id, Name: "f"}})
				}
				require.Len(t, events, 1, "second added must not close the first tool")
				switch insert {
				case "text":
					feed(&ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 2, Text: "after"})
				case "thinking":
					feed(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 2, Item: &ResponsesOutput{Type: "reasoning", ID: "r"}})
					feed(&ResponsesStreamEvent{Type: "response.reasoning_summary_text.delta", OutputIndex: 2, Delta: "thought"})
				case "search":
					feed(&ResponsesStreamEvent{Type: "response.output_item.done", OutputIndex: 2, Item: &ResponsesOutput{Type: "web_search_call", ID: "w", Status: "completed"}})
				}
				order := []int{0, 1}
				if reverse {
					order = []int{1, 0}
				}
				for _, i := range order {
					id := []string{"fc_a", "fc_b"}[i]
					args := []string{`{"x":1}`, `{"y":2}`}[i]
					e := m3ToolEvent("response.function_call_arguments.done", i, id, args)
					feed(e)
					feed(e)
				}
				feed(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}})
				require.NoError(t, st.Err())
				got := map[string]string{}
				open := map[int]string{}
				closed := map[int]bool{}
				for _, e := range events {
					if e.Index == nil {
						continue
					}
					i := *e.Index
					switch e.Type {
					case "content_block_start":
						require.False(t, closed[i])
						open[i] = e.ContentBlock.ID
					case "content_block_delta":
						_, ok := open[i]
						require.True(t, ok)
						require.False(t, closed[i])
						if e.Delta.Type == "input_json_delta" {
							got[open[i]] += e.Delta.PartialJSON
						}
					case "content_block_stop":
						_, ok := open[i]
						require.True(t, ok)
						require.False(t, closed[i])
						closed[i] = true
					}
				}
				require.Equal(t, map[string]string{"call_a": `{"x":1}`, "call_b": `{"y":2}`}, got)
				require.Len(t, closed, len(open))
			})
		}
	}
	t.Run("duplicate call ID and conflicting identity", func(t *testing.T) {
		st := NewResponsesEventToAnthropicState()
		for i, id := range []string{"fc_a", "fc_b"} {
			ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: i, OutputIndexPresent: true, Item: &ResponsesOutput{Type: "function_call", ID: id, CallID: "reused", Name: "f"}}, st)
		}
		ResponsesEventToAnthropicEvents(m3ToolEvent("response.function_call_arguments.done", 1, "fc_b", `{"y":2}`), st)
		require.NoError(t, st.Err())
		ResponsesEventToAnthropicEvents(m3ToolEvent("response.function_call_arguments.done", 1, "fc_a", `{}`), st)
		require.Error(t, st.Err())
		require.Empty(t, FinalizeResponsesAnthropicStream(st))
	})
}

func TestM3FunctionArgumentsFinalValue(t *testing.T) {
	for _, tc := range []struct {
		name, tool, delta, final, want string
		bad                            bool
	}{
		{"same", "f", `{"x":1}`, `{"x":1}`, `{"x":1}`, false},
		{"tail", "f", `{"x":`, `{"x":1}`, `{"x":1}`, false},
		{"mismatch", "f", `{"x":1}`, `{"x":2}`, `{"x":1}`, true},
		{"no delta", "f", "", `{"x":1}`, `{"x":1}`, false},
		{"Read", "Read", `{"file_path":"x","pages":""}`, `{"file_path":"x","pages":""}`, `{"file_path":"x"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewResponsesEventToAnthropicState()
			var out []AnthropicStreamEvent
			feed := func(e *ResponsesStreamEvent) { out = append(out, ResponsesEventToAnthropicEvents(e, st)...) }
			feed(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &ResponsesOutput{Type: "function_call", ID: "fc_a", CallID: "call_a", Name: tc.tool}})
			feed(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", ItemID: "fc_a", Delta: tc.delta})
			feed(m3ToolEvent("response.function_call_arguments.done", 0, "fc_a", tc.final))
			var got string
			for _, e := range out {
				if e.Delta != nil && e.Delta.Type == "input_json_delta" {
					got += e.Delta.PartialJSON
				}
			}
			require.Equal(t, tc.want, got)
			if tc.bad {
				require.Error(t, st.Err())
				require.Equal(t, tc.delta, st.tools[0].raw)
				require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, st))
				return
			}
			require.NoError(t, st.Err())
			require.Empty(t, ResponsesEventToAnthropicEvents(m3ToolEvent("response.function_call_arguments.done", 0, "fc_a", tc.final), st))
			ResponsesEventToAnthropicEvents(m3ToolEvent("response.function_call_arguments.done", 0, "fc_a", `{"changed":true}`), st)
			require.Error(t, st.Err())
		})
	}
	t.Run("terminal mismatch", func(t *testing.T) {
		st := NewResponsesEventToAnthropicState()
		ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &ResponsesOutput{Type: "function_call", ID: "fc_a", CallID: "call_a", Name: "f"}}, st)
		ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", ItemID: "fc_a", Delta: `{"x":1}`}, st)
		events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "function_call", ID: "fc_a", Arguments: `{"x":2}`}}}}, st)
		require.Error(t, st.Err())
		require.Empty(t, events)
	})
}

func TestM3FunctionArgumentValidation(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"object", `{"x":1}`, true}, {"Read", `{"file_path":"x","pages":""}`, true}, {"bad", `{"x":`, false},
		{"array", `[]`, false}, {"null", `null`, false}, {"scalar", `1`, false}, {"empty", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "f"
			if tc.name == "Read" {
				name = "Read"
			}
			item := ResponsesOutput{Type: "function_call", ID: "fc_a", CallID: "call_a", Name: name, Arguments: tc.raw}
			resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{item}}
			err := ValidateResponsesForAnthropic(resp)
			if tc.valid {
				require.NoError(t, err)
				require.NotNil(t, ResponsesToAnthropic(resp, "test"))
			} else {
				require.Error(t, err)
				require.Nil(t, ResponsesToAnthropic(resp, "test"))
			}
			st := NewResponsesEventToAnthropicState()
			item.Arguments = ""
			ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &item}, st)
			done := ResponsesEventToAnthropicEvents(m3ToolEvent("response.function_call_arguments.done", 0, "fc_a", tc.raw), st)
			if tc.valid {
				require.NoError(t, st.Err())
				if tc.raw == "" {
					require.Empty(t, done)
				}
				ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.completed", Response: resp}, st)
				require.NoError(t, st.Err())
			} else {
				require.Error(t, st.Err())
				require.Empty(t, done)
				require.Equal(t, tc.raw, st.tools[0].raw)
				require.Empty(t, FinalizeResponsesAnthropicStream(st))
			}
		})
	}
	t.Run("truncated empty", func(t *testing.T) {
		resp := &ResponsesResponse{Status: "incomplete", Output: []ResponsesOutput{{Type: "function_call", CallID: "call_a", Name: "f"}}}
		require.Error(t, ValidateResponsesForAnthropic(resp))
		st := NewResponsesEventToAnthropicState()
		ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &resp.Output[0]}, st)
		events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.incomplete", Response: resp}, st)
		require.Error(t, st.Err())
		require.Empty(t, events)
	})
	t.Run("custom empty", func(t *testing.T) {
		require.NoError(t, ValidateResponsesForAnthropic(&ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "custom_tool_call", Input: ""}}}))
	})
	t.Run("opaque stays opaque", func(t *testing.T) {
		resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "function_call", Arguments: "cipher", EncryptedFunctionArgs: json.RawMessage(`"opaque"`)}}}
		var err *UpstreamConversionError
		require.ErrorAs(t, ValidateResponsesForAnthropic(resp), &err)
		require.Equal(t, "encrypted_function_args", err.Field)
		require.Equal(t, "cipher", resp.Output[0].Arguments)
	})
}

func TestM3CustomToolResponse(t *testing.T) {
	for _, input := range []string{"patch\ntext", "", "你好 🌍"} {
		for _, carrier := range []string{"delta done", "done", "item", "terminal"} {
			t.Run(input+"/"+carrier, func(t *testing.T) {
				item := ResponsesOutput{Type: "custom_tool_call", ID: "ct_a", CallID: "call_a", Name: "patch", Input: input, Status: "completed"}
				resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{item}}
				nonstream := ResponsesToAnthropic(resp, "test")
				require.Len(t, nonstream.Content, 1)
				var wrapped map[string]string
				require.NoError(t, json.Unmarshal(nonstream.Content[0].Input, &wrapped))
				require.Equal(t, input, wrapped["input"])
				st := NewResponsesEventToAnthropicState()
				var events []AnthropicStreamEvent
				feed := func(e *ResponsesStreamEvent) { events = append(events, ResponsesEventToAnthropicEvents(e, st)...) }
				if carrier != "terminal" {
					added := item
					added.Input = ""
					feed(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &added})
				}
				if carrier == "delta done" {
					feed(&ResponsesStreamEvent{Type: "response.custom_tool_call_input.delta", ItemID: item.ID, Delta: input})
				}
				if carrier == "done" || carrier == "delta done" {
					feed(&ResponsesStreamEvent{Type: "response.custom_tool_call_input.done", ItemID: item.ID, Input: input, InputPresent: true})
				}
				if carrier == "item" {
					feed(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &item})
				}
				feed(&ResponsesStreamEvent{Type: "response.completed", Response: resp})
				require.NoError(t, st.Err())
				var raw string
				var stops int
				for _, e := range events {
					if e.Delta != nil && e.Delta.Type == "input_json_delta" {
						raw += e.Delta.PartialJSON
					}
					if e.Type == "content_block_stop" {
						stops++
					}
				}
				require.JSONEq(t, string(nonstream.Content[0].Input), raw)
				require.Equal(t, 1, stops)
			})
		}
	}
	require.NoError(t, ValidateResponsesForAnthropic(&ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "function_call", Arguments: `{"input":"normal"}`}}}))
}

func TestM3WebSearchSources(t *testing.T) {
	for _, tc := range []struct{ name, status, action, want string }{
		{"missing", "completed", `null`, `[]`}, {"empty", "completed", `{"type":"search","sources":[]}`, `[]`},
		{"sources", "completed", `{"type":"search","sources":[{"url":"https://example.invalid/a","title":"A","content":"body"},{"url":"https://example.invalid/a","title":"duplicate"},{"url":"javascript:bad"},{"url":"http://example.invalid/b"},{"url":"https://"}]}`, `[{"type":"web_search_result","url":"https://example.invalid/a","title":"A","content":"body"},{"type":"web_search_result","url":"http://example.invalid/b"}]`},
		{"open page", "completed", `{"type":"open_page","url":"https://example.invalid/page"}`, `[{"type":"web_search_result","url":"https://example.invalid/page"}]`},
		{"unfinished", "in_progress", `{"type":"search","sources":[{"url":"https://example.invalid/a"}]}`, `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := ResponsesOutput{Type: "web_search_call", ID: "ws_a", Status: tc.status}
			require.NoError(t, json.Unmarshal([]byte(tc.action), &item.Action))
			require.JSONEq(t, tc.want, string(responsesWebSearchSources(item)))
			resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{item}}
			out := ResponsesToAnthropic(resp, "test")
			st := NewResponsesEventToAnthropicState()
			events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &item}, st)
			events = append(events, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{Type: "response.completed", Response: resp}, st)...)
			var jsonResults, streamResults []json.RawMessage
			for _, b := range out.Content {
				if b.Type == "web_search_tool_result" {
					jsonResults = append(jsonResults, b.Content)
				}
			}
			for _, e := range events {
				if e.ContentBlock != nil && e.ContentBlock.Type == "web_search_tool_result" {
					streamResults = append(streamResults, e.ContentBlock.Content)
				}
			}
			require.Equal(t, jsonResults, streamResults)
			if tc.status == "completed" {
				require.Len(t, jsonResults, 1)
				require.JSONEq(t, tc.want, string(jsonResults[0]))
			} else {
				require.Empty(t, jsonResults)
			}
		})
	}
}
