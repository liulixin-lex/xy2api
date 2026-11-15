//go:build unit

package apicompat

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// These fixed snapshots were captured at 1a0513330 from the unchanged R3
// fixtures: 21 paired cases, 5 unpaired cases and the existing encoding error.
// Source JSON and both legacy projections are compared byte for byte.
func TestM4ToolHistoryBaseline(t *testing.T) {
	var fixtures []struct {
		Name           string
		Messages       []ChatMessage
		Input          json.RawMessage
		SourceError    string
		Chat           json.RawMessage
		ChatError      string
		Anthropic      json.RawMessage
		AnthropicError string
	}
	raw, err := os.ReadFile("testdata/m4-history-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 27 {
		t.Fatalf("expected 27 fixed cases, got %d", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			request, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: "probe", Messages: fixture.Messages})
			m4CheckError(t, err, fixture.SourceError)
			if err != nil {
				return
			}
			m4CheckJSONBytes(t, request.Input, fixture.Input)
			chat, err := ResponsesToChatCompletionsRequest(request)
			m4CheckError(t, err, fixture.ChatError)
			if err == nil {
				actual, e := json.Marshal(chat)
				if e != nil {
					t.Fatal(e)
				}
				m4CheckJSONBytes(t, actual, fixture.Chat)
			}
			anthropic, err := ResponsesToAnthropicRequest(request)
			m4CheckError(t, err, fixture.AnthropicError)
			if err == nil {
				actual, e := json.Marshal(anthropic)
				if e != nil {
					t.Fatal(e)
				}
				m4CheckJSONBytes(t, actual, fixture.Anthropic)
			}
		})
	}
}

func m4CheckError(t *testing.T, err error, expected string) {
	t.Helper()
	actual := ""
	if err != nil {
		actual = err.Error()
	}
	if actual != expected {
		t.Fatalf("error=%q want=%q", actual, expected)
	}
}

func m4CheckJSONBytes(t *testing.T, actual, expected []byte) {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, expected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, compact.Bytes()) {
		t.Fatalf("actual=%s\nexpected=%s", actual, compact.Bytes())
	}
}

func TestM4TypedChatOutputDecoder(t *testing.T) {
	cases := []struct {
		raw  string
		want []ChatOutputPart
		bad  bool
	}{
		{`"answer"`, []ChatOutputPart{{"text", "answer"}}, false},
		{`null`, nil, false},
		{`[{"type":"thinking","thinking":"THINK"},{"type":"text","text":"ANSWER"},{"type":"thinking","thinking":{"text":"NEXT"}}]`, []ChatOutputPart{{"thinking", "THINK"}, {"text", "ANSWER"}, {"thinking", "NEXT"}}, false},
		{`[{"type":"unknown","text":"x"}]`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := DecodeChatOutputContent(json.RawMessage(tc.raw))
			if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, %v; want %#v, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
	raw := []byte(`{"id":"chunk","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"THINK"},{"type":"text","text":"ANSWER"}]}}]}`)
	original := string(raw)
	chunk, arrays, err := DecodeChatBridgeChunk(raw)
	if err != nil || chunk.ID != "chunk" || len(arrays) != 1 || string(raw) != original {
		t.Fatalf("bridge chunk: %#v %#v %v", chunk, arrays, err)
	}
	var legacy ChatCompletionsChunk
	if json.Unmarshal(raw, &legacy) == nil {
		t.Fatal("default ChatDelta unexpectedly accepted arrays")
	}
	for _, raw := range []string{`{"choices":[{"index":0,"finish_reason":"stop"}]}`, `{"choices":[{"index":0,"delta":null}]}`, `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`} {
		if _, _, err := DecodeChatBridgeChunk([]byte(raw)); err != nil {
			t.Fatalf("ordinary chunk rejected: %s: %v", raw, err)
		}
	}
}

func TestM4ToolAliasAllocation(t *testing.T) {
	occupied := make(map[string]string)
	for _, name := range []string{"lookup", strings.Repeat("a", 64), strings.Repeat("a", 65), "工具"} {
		alias := bridgeToolAlias(name, name, 64, occupied)
		if !validBridgeToolName(alias, 64) || bridgeToolAlias(name, name, 64, occupied) != alias {
			t.Fatalf("unstable or illegal alias %q", alias)
		}
		if validBridgeToolName(name, 64) && alias != name {
			t.Fatalf("valid name changed: %q", alias)
		}
	}
	first := bridgeToolAlias("function/ns/x", "ns__x", 64, occupied)
	second := bridgeToolAlias("custom/ns/x", "ns__x", 64, occupied)
	if first == second {
		t.Fatal("kinds collided")
	}
	occupied[second] = "occupied by another original name"
	if bridgeToolAlias("custom/ns/x", "ns__x", 64, occupied) == second {
		t.Fatal("collision was not salted")
	}
}

func TestM4ToolDeclarationMergeCore(t *testing.T) {
	batches := [][]json.RawMessage{
		{json.RawMessage(`{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}},"strict":false}`), json.RawMessage(`{"type":"namespace","name":"n","tools":[{"type":"function","name":"a"}]}`)},
		{json.RawMessage(`{"type":"function","name":"lookup","parameters":{"properties":{},"type":"object"},"strict":false}`), json.RawMessage(`{"type":"namespace","name":"n","tools":[{"type":"function","name":"b"},{"type":"custom","name":"a"}]}`)},
		{json.RawMessage(`{"type":"function","name":"lookup","parameters":{"type":"string"},"strict":true}`), json.RawMessage(`{"type":"custom","name":"lookup","format":{"type":"text"}}`), json.RawMessage(`{"type":"function","name":"same"}`), json.RawMessage(`{"type":"function","name":"same"}`)},
	}
	var notices []string
	merged, err := mergeBridgeToolBatches(batches, func(field string) { notices = append(notices, field) })
	if err != nil || len(merged) != 5 {
		t.Fatalf("merged=%s err=%v", merged, err)
	}
	var tools []ResponsesTool
	raw, _ := json.Marshal(merged)
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools[1].Tools) != 3 || !bridgeSchemaEqual(tools[0].Parameters, json.RawMessage(`{"type":"object","properties":{}}`)) {
		t.Fatalf("lost first definition/namespace children: %s", raw)
	}
	if !reflect.DeepEqual(notices, []string{"input[1].tools[0].parameters", "input[1].tools[0].strict"}) {
		t.Fatalf("notices=%v", notices)
	}
}

func TestM4ChatToolSlotResolver(t *testing.T) {
	var slots chatToolSlots
	first, err := ResolveChatToolSlot(0, 0, nil, "a", &slots)
	if err != nil || first != 0 {
		t.Fatalf("first=%d %v", first, err)
	}
	second, err := ResolveChatToolSlot(0, 1, nil, "b", &slots)
	if err != nil || second != 1 {
		t.Fatalf("second=%d %v", second, err)
	}
	index := 2
	bound, err := ResolveChatToolSlot(0, 0, &index, "b", &slots)
	if err != nil || bound != second {
		t.Fatalf("late index=%d %v", bound, err)
	}
	if _, err := ResolveChatToolSlot(0, 0, nil, "", &slots); err == nil {
		t.Fatal("ambiguous position accepted")
	}
	if _, err := ResolveChatToolSlot(1, 0, nil, "", &slots); err == nil {
		t.Fatal("nonzero choice accepted")
	}
	index = -1
	if _, err := ResolveChatToolSlot(0, 0, &index, "", &slots); err == nil {
		t.Fatal("negative index accepted")
	}
}

func TestM4ToolDeclarationMerge(t *testing.T) {
	req := &ResponsesRequest{Model: "probe", Input: json.RawMessage(`[{"role":"user","content":"hi"},{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}},{"type":"function","name":"other"}]}]`), Tools: []ResponsesTool{{Type: "function", Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	var notices []ConversionNotice
	out, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}, Record: func(n ConversionNotice) { notices = append(notices, n) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 2 || string(out.Tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("tools=%+v", out.Tools)
	}
	if !reflect.DeepEqual(notices, []ConversionNotice{{Field: "input[1].tools[0].parameters", Action: "omit"}}) {
		t.Fatalf("notices=%v", notices)
	}
	if _, err := ResponsesToChatCompletionsRequest(req); err == nil {
		t.Fatal("legacy duplicate rejection changed")
	}
}

func TestM4ToolNameRoundTrip(t *testing.T) {
	names := []string{"lookup", strings.Repeat("a", 64), strings.Repeat("a", 65), "工具"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			input, _ := json.Marshal([]map[string]any{{"type": "function_call", "call_id": "c", "name": name, "arguments": "{}"}, {"type": "function_call_output", "call_id": "c", "output": "ok"}, {"type": "function_call", "call_id": "old", "name": "historical工具", "arguments": "{}"}, {"type": "function_call_output", "call_id": "old", "output": "old"}})
			choice, _ := json.Marshal(map[string]string{"type": "function", "name": name})
			bridge := &BridgeOptions{Enabled: true}
			req := &ResponsesRequest{Model: "probe", Input: input, Tools: []ResponsesTool{{Type: "function", Name: name}}, ToolChoice: choice}
			got, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: bridge})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tools) != 1 || !validBridgeToolName(got.Tools[0].Function.Name, 64) {
				t.Fatalf("tools=%+v", got.Tools)
			}
			alias := got.Tools[0].Function.Name
			if !strings.Contains(string(got.ToolChoice), alias) || got.Messages[0].ToolCalls[0].Function.Name != alias {
				t.Fatalf("choice/history not mapped: %+v", got)
			}
			if validBridgeToolName(name, 64) && alias != name {
				t.Fatalf("valid original renamed: %s", alias)
			}
			response := &ChatCompletionsResponse{ID: "fixed", Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{{ID: "c", Function: ChatFunctionCall{Name: alias, Arguments: "{}"}}}}, FinishReason: "tool_calls"}}}
			out, err := ChatCompletionsResponseToResponsesWithBridgeOptions(response, "probe", nil, nil, false, nil, ConversionOptions{Bridge: bridge})
			if err != nil {
				t.Fatal(err)
			}
			if out.Output[0].Name != name {
				t.Fatalf("lost original identity: %+v", out.Output)
			}
			if len(req.Tools) != 1 || string(req.Input) != string(input) {
				t.Fatal("mutated input")
			}
		})
	}
	tools := []ResponsesTool{{Type: "function", Name: "n__f"}, {Type: "namespace", Name: "n", Tools: []ResponsesTool{{Type: "function", Name: "f"}}}, {Type: "namespace", Name: "other", Tools: []ResponsesTool{{Type: "function", Name: "f"}}}}
	mapping, err := BuildBridgeToolNames(tools, nil, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping.Reverse) != 3 || mapping.Forward[ToolNameIdentity{Kind: "function", Name: "n__f"}] != "n__f" {
		t.Fatalf("collision table=%+v", mapping)
	}
	again, err := BuildBridgeToolNames(tools, nil, "chat")
	if err != nil || !reflect.DeepEqual(mapping, again) {
		t.Fatal("nondeterministic table")
	}
}

func TestM4NamespaceCustomRoundTrip(t *testing.T) {
	var req map[string]any
	if err := json.Unmarshal([]byte(`{"model":"probe","tools":[{"type":"namespace","name":"mcp_tools","tools":[{"type":"custom","name":"exec","format":{"type":"text"}},{"type":"function","name":"exec","parameters":{"type":"object"}}]}],"tool_choice":{"type":"custom","namespace":"mcp_tools","name":"exec"},"input":[{"type":"custom_tool_call","call_id":"old","namespace":"retired","name":"old_exec","input":"old"},{"type":"custom_tool_call_output","call_id":"old","output":"ok"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	bridge := &BridgeOptions{Enabled: true}
	mapping, _, err := AdaptResponsesClientToolsWithBridgeOptions(req, ConversionOptions{Bridge: bridge})
	if err != nil {
		t.Fatal(err)
	}
	tools := req["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools=%v", tools)
	}
	alias := bridge.ToolNames.Forward[ToolNameIdentity{Kind: "custom", Namespace: "mcp_tools", Name: "exec"}]
	if tools[0].(map[string]any)["name"] != alias || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("custom not lowered: %v", tools)
	}
	history := req["input"].([]any)[0].(map[string]any)
	if history["type"] != "function_call" || history["arguments"] != `{"input":"old"}` {
		t.Fatalf("history=%v", history)
	}
	payload, _ := json.Marshal(map[string]any{"output": []any{map[string]any{"type": "function_call", "id": "fc_fixed", "call_id": "c", "name": alias, "arguments": `{"input":"echo ok"}`, "status": "completed"}}})
	restored, _, err := RestoreResponsesClientToolPayload(payload, mapping)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []ResponsesOutput `json:"output"`
	}
	if err := json.Unmarshal(restored, &response); err != nil {
		t.Fatal(err)
	}
	item := response.Output[0]
	if item.Type != "custom_tool_call" || item.Name != "exec" || item.Namespace != "mcp_tools" || item.Input != "echo ok" {
		t.Fatalf("restored=%s", restored)
	}
	restorer := NewResponsesClientToolStreamRestorer(mapping)
	var events []ResponsesStreamEvent
	for _, event := range []ResponsesStreamEvent{
		{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "fc_fixed", CallID: "c", Name: alias}},
		{Type: "response.function_call_arguments.delta", OutputIndex: 0, ItemID: "fc_fixed", Delta: `{"input":"echo ok"}`},
		{Type: "response.function_call_arguments.done", OutputIndex: 0, ItemID: "fc_fixed", Arguments: `{"input":"echo ok"}`},
		{Type: "response.output_item.done", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "fc_fixed", CallID: "c", Name: alias, Arguments: `{"input":"echo ok"}`, Status: "completed"}},
	} {
		events = append(events, restorer.Restore(event)...)
	}
	for _, event := range events {
		if event.Item != nil && (event.Item.Type != "custom_tool_call" || event.Item.Namespace != "mcp_tools" || event.Item.Name != "exec") {
			t.Fatalf("event=%+v item=%+v", event, event.Item)
		}
		if event.Type == "response.custom_tool_call_input.done" && (event.Name != "exec" || event.Input != "echo ok") {
			t.Fatalf("input done=%+v", event)
		}
	}
}

func TestM4ChatToolSlots(t *testing.T) {
	for _, calls := range []string{
		`[{"id":"a","function":{"name":"one","arguments":"{}"}},{"id":"b","function":{"name":"two","arguments":"{}"}}]`,
		`[{"index":2,"id":"a","function":{"name":"one","arguments":"{}"}}]`,
	} {
		t.Run(calls, func(t *testing.T) {
			var chunk ChatCompletionsChunk
			if err := json.Unmarshal([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":`+calls+`},"finish_reason":"tool_calls"}]}`), &chunk); err != nil {
				t.Fatal(err)
			}
			rs := NewChatCompletionsToResponsesStreamState("m")
			rs.Bridge = &BridgeOptions{Enabled: true}
			events := ChatCompletionsChunkToResponsesEvents(&chunk, rs)
			events = append(events, FinalizeChatCompletionsResponsesStream(rs)...)
			if rs.Err() != nil {
				t.Fatal(rs.Err())
			}
			added, done := map[string]int{}, map[string]int{}
			for _, e := range events {
				if e.Item != nil {
					if e.Type == "response.output_item.added" {
						added[e.Item.ID] = e.OutputIndex
					}
					if e.Type == "response.output_item.done" {
						done[e.Item.ID] = e.OutputIndex
					}
				}
			}
			if len(added) != len(chunk.Choices[0].Delta.ToolCalls) || !reflect.DeepEqual(added, done) {
				t.Fatalf("added=%v done=%v", added, done)
			}
			terminal := events[len(events)-1].Response
			for i, item := range terminal.Output {
				if added[item.ID] != i {
					t.Fatalf("terminal identity/index changed: %+v", terminal.Output)
				}
			}
			as := NewChatCompletionsToAnthropicStreamState("m")
			as.Bridge = &BridgeOptions{Enabled: true}
			ae := ChatCompletionsChunkToAnthropicEvents(&chunk, as)
			ae = append(ae, FinalizeChatCompletionsAnthropicStream(as)...)
			started := 0
			for _, e := range ae {
				if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" {
					started++
				}
			}
			if as.Err() != nil || started != len(chunk.Choices[0].Delta.ToolCalls) {
				t.Fatalf("starts=%d err=%v", started, as.Err())
			}
		})
	}
	rs := NewChatCompletionsToResponsesStreamState("m")
	rs.Bridge = &BridgeOptions{Enabled: true}
	as := NewChatCompletionsToAnthropicStreamState("m")
	as.Bridge = &BridgeOptions{Enabled: true}
	for _, raw := range []string{`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"function":{"name":"one","arguments":"{"}}]}}]}`, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"late","function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`} {
		var chunk ChatCompletionsChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, e := range ChatCompletionsChunkToResponsesEvents(&chunk, rs) {
			if e.Item != nil && e.Item.CallID != "late" {
				t.Fatalf("premature ID: %+v", e.Item)
			}
		}
		for _, e := range ChatCompletionsChunkToAnthropicEvents(&chunk, as) {
			if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" && e.ContentBlock.ID != "late" {
				t.Fatalf("premature ID: %+v", e.ContentBlock)
			}
		}
	}
	if rs.Err() != nil || as.Err() != nil {
		t.Fatalf("late identity failed: %v %v", rs.Err(), as.Err())
	}
	bad := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Index: 1}}}
	ChatCompletionsChunkToResponsesEvents(bad, rs)
	ChatCompletionsChunkToAnthropicEvents(bad, as)
	if rs.Err() == nil || as.Err() == nil || len(FinalizeChatCompletionsResponsesStream(rs)) != 0 || len(FinalizeChatCompletionsAnthropicStream(as)) != 0 {
		t.Fatal("nonzero choice completed")
	}
}
