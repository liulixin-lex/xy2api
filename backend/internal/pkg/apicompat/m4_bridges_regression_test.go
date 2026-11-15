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

func TestM4AnthropicToolInterleave(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(map[bool]string{true: "finish", false: "EOF"}[finish], func(t *testing.T) {
			state := NewChatCompletionsToAnthropicStreamState("m")
			state.Bridge = &BridgeOptions{Enabled: true}
			var events []AnthropicStreamEvent
			for _, delta := range []string{
				`{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"{"}}]}`,
				`{"content":"between"}`,
				`{"tool_calls":[{"index":1,"id":"b","function":{"arguments":"{}"}}]}`,
				`{"reasoning_content":"THINK"}`,
				`{"tool_calls":[{"index":0,"function":{"arguments":"}"}},{"index":1,"function":{"name":"second"}}]}`,
			} {
				var chunk ChatCompletionsChunk
				if err := json.Unmarshal([]byte(`{"choices":[{"index":0,"delta":`+delta+`}]}`), &chunk); err != nil {
					t.Fatal(err)
				}
				events = append(events, ChatCompletionsChunkToAnthropicEvents(&chunk, state)...)
			}
			if finish {
				reason := "tool_calls"
				events = append(events, ChatCompletionsChunkToAnthropicEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{FinishReason: &reason}}}, state)...)
			}
			events = append(events, FinalizeChatCompletionsAnthropicStream(state)...)
			open := map[int]bool{}
			started := map[int]bool{}
			stopped := false
			var order []string
			for _, e := range events {
				switch e.Type {
				case "content_block_start":
					if started[*e.Index] {
						t.Fatal("duplicate start")
					}
					started[*e.Index] = true
					open[*e.Index] = true
					order = append(order, e.ContentBlock.Type+":"+e.ContentBlock.Name)
				case "content_block_delta":
					if !open[*e.Index] {
						t.Fatalf("delta on closed block: %+v", e)
					}
				case "content_block_stop":
					if !open[*e.Index] {
						t.Fatal("duplicate stop")
					}
					delete(open, *e.Index)
				case "message_stop":
					stopped = true
				}
			}
			if finish {
				if state.Err() != nil || len(open) != 0 || !stopped {
					t.Fatalf("err=%v open=%v stopped=%v", state.Err(), open, stopped)
				}
				if !reflect.DeepEqual(order, []string{"tool_use:first", "text:", "tool_use:second", "thinking:"}) {
					t.Fatalf("order=%v", order)
				}
			} else if state.Err() == nil || stopped {
				t.Fatal("EOF completed tool input")
			}
		})
	}
}

func TestM4ChatToolTerminalIdentityE14(t *testing.T) {
	for _, kind := range []string{"function", "custom", "tool_search"} {
		t.Run(kind, func(t *testing.T) {
			state := NewChatCompletionsToResponsesStreamState("m")
			name := "lookup"
			arguments := "{}"
			if kind == "custom" {
				name = "exec"
				arguments = `{"input":"echo ok"}`
				state.CustomTools = map[string]bool{name: true}
			}
			if kind == "tool_search" {
				name = toolSearchProxyName
				state.ToolSearchDeclared = true
			}
			index := 0
			reason := "tool_calls"
			chunk := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: []ChatToolCall{{Index: &index, ID: "c", Function: ChatFunctionCall{Name: name, Arguments: arguments}}}}, FinishReason: &reason}}}
			events := ChatCompletionsChunkToResponsesEvents(chunk, state)
			events = append(events, FinalizeChatCompletionsResponsesStream(state)...)
			added, done, terminal := "", "", ""
			for _, e := range events {
				if e.Item != nil {
					if e.Type == "response.output_item.added" {
						added = e.Item.ID
					}
					if e.Type == "response.output_item.done" {
						done = e.Item.ID
					}
				}
				if e.Type == "response.completed" {
					terminal = e.Response.Output[0].ID
				}
			}
			if added == "" || added != done || added != terminal {
				t.Fatalf("added=%q done=%q terminal=%q", added, done, terminal)
			}
		})
	}
	state := NewChatCompletionsToResponsesStreamState("m")
	state.MessageItemID = "fixed"
	_, _ = state.Text.WriteString("answer")
	raw, err := json.Marshal(state.chatOutput())
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"message","id":"fixed","role":"assistant","content":[{"type":"output_text","text":"answer"}],"status":"completed"}]`
	if string(raw) != want {
		t.Fatalf("no-tool output changed: %s", raw)
	}
}

func TestM4ReasoningIdentity(t *testing.T) {
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
	chat, err := ChatCompletionsResponseToResponsesWithBridgeOptions(&ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ReasoningContent: "THINK", Content: json.RawMessage(`"ANSWER"`)}, FinishReason: "stop"}}}, "m", nil, nil, false, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	anth, err := AnthropicToResponsesResponseWithBridgeOptions(&AnthropicResponse{Content: []AnthropicContentBlock{{Type: "thinking", Thinking: "THINK"}}, StopReason: AnthropicStopReasonPtr("end_turn")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []*ResponsesResponse{chat, anth} {
		if !strings.HasPrefix(response.Output[0].ID, "rs_") {
			t.Fatalf("ID=%s", response.Output[0].ID)
		}
	}
	rs := NewChatCompletionsToResponsesStreamState("m")
	rs.Bridge = opts.Bridge
	text := "THINK"
	answer := "ANSWER"
	reason := "stop"
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ReasoningContent: &text}}}}, rs)
	events = append(events, ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: &answer}, FinishReason: &reason}}}, rs)...)
	events = append(events, FinalizeChatCompletionsResponsesStream(rs)...)
	m4AssertReasoningIdentity(t, events)
	state := NewAnthropicEventToResponsesState()
	var ae []ResponsesStreamEvent
	for _, event := range []*AnthropicStreamEvent{{Type: "message_start", Message: &AnthropicResponse{ID: "fixed", Model: "m"}}, {Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "thinking"}}, {Type: "content_block_delta", Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "THINK"}}, {Type: "content_block_stop"}, {Type: "message_delta", Delta: &AnthropicDelta{StopReason: "end_turn"}}, {Type: "message_stop"}} {
		ae = append(ae, AnthropicEventToResponsesEventsWithBridgeOptions(event, state, opts)...)
	}
	m4AssertReasoningIdentity(t, ae)
}

func m4AssertReasoningIdentity(t *testing.T, events []ResponsesStreamEvent) {
	t.Helper()
	id := ""
	index := -1
	for _, e := range events {
		if e.Item != nil && e.Item.Type == "reasoning" {
			if id == "" {
				id = e.Item.ID
				index = e.OutputIndex
			}
			if e.Item.ID != id || e.OutputIndex != index {
				t.Fatalf("item identity changed: %+v", e)
			}
		}
		if strings.HasPrefix(e.Type, "response.reasoning_summary_") && (e.ItemID != id || e.OutputIndex != index) {
			t.Fatalf("summary identity changed: %+v", e)
		}
		if e.Response != nil && e.Response.Status == "completed" {
			found := false
			for i, item := range e.Response.Output {
				if item.Type == "reasoning" {
					found = true
					if item.ID != id || i != index {
						t.Fatalf("terminal identity changed: %+v", item)
					}
				}
			}
			if !found {
				t.Fatal("terminal reasoning missing")
			}
		}
	}
	if !strings.HasPrefix(id, "rs_") {
		t.Fatalf("ID=%s", id)
	}
}

func TestM4AnthropicSummaryLifecycle(t *testing.T) {
	for _, ending := range []string{"block_stop", "next_item", "message_stop", "EOF", "signature_only"} {
		t.Run(ending, func(t *testing.T) {
			state := NewAnthropicEventToResponsesState()
			opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
			var events []ResponsesStreamEvent
			emit := func(e *AnthropicStreamEvent) {
				events = append(events, AnthropicEventToResponsesEventsWithBridgeOptions(e, state, opts)...)
			}
			emit(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "fixed", Model: "m"}})
			text := "FIRST"
			if ending == "signature_only" {
				text = ""
			}
			emit(&AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "thinking", Thinking: text, Signature: "signature"}})
			if ending != "signature_only" {
				emit(&AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "SECOND"}})
			}
			switch ending {
			case "block_stop", "signature_only":
				emit(&AnthropicStreamEvent{Type: "content_block_stop"})
			case "next_item":
				emit(&AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text"}})
			case "message_stop":
				emit(&AnthropicStreamEvent{Type: "message_delta", Delta: &AnthropicDelta{StopReason: "end_turn"}})
				emit(&AnthropicStreamEvent{Type: "message_stop"})
			case "EOF":
				events = append(events, FinalizeAnthropicResponsesStreamWithBridgeOptions(state, opts)...)
			}
			counts := map[string]int{}
			id := ""
			var order []string
			for _, e := range events {
				if e.Item != nil && e.Item.Type == "reasoning" {
					if id == "" {
						id = e.Item.ID
					}
					if e.Item.ID != id {
						t.Fatal("ID changed")
					}
					if e.Type == "response.output_item.done" && ending != "signature_only" && e.Item.Summary[0].Text != "FIRSTSECOND" {
						t.Fatal("summary differs")
					}
				}
				if strings.HasPrefix(e.Type, "response.reasoning_summary_") {
					counts[e.Type]++
					order = append(order, e.Type)
					if e.ItemID != id || e.OutputIndex != 0 {
						t.Fatal("summary identity differs")
					}
					if e.Type == "response.reasoning_summary_text.done" && e.Text != "FIRSTSECOND" {
						t.Fatalf("done=%q", e.Text)
					}
				}
			}
			if ending == "signature_only" {
				if len(counts) != 0 {
					t.Fatalf("empty part created: %v", counts)
				}
				return
			}
			if counts["response.reasoning_summary_part.added"] != 1 || counts["response.reasoning_summary_text.done"] != 1 || counts["response.reasoning_summary_part.done"] != 1 {
				t.Fatalf("counts=%v", counts)
			}
			if len(order) != 5 || order[0] != "response.reasoning_summary_part.added" || order[3] != "response.reasoning_summary_text.done" || order[4] != "response.reasoning_summary_part.done" {
				t.Fatalf("order=%v", order)
			}
		})
	}
}

func TestM4LateReasoningSealed(t *testing.T) {
	for _, first := range []string{"text", "tool", "reasoning_then_text", "reasoning_only"} {
		t.Run(first, func(t *testing.T) {
			state := NewChatCompletionsToResponsesStreamState("m")
			state.Bridge = &BridgeOptions{Enabled: true}
			var events []ResponsesStreamEvent
			emit := func(delta ChatDelta) {
				events = append(events, ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: delta}}}, state)...)
			}
			before := "BEFORE"
			after := "LATE"
			text := "ANSWER"
			if first == "reasoning_then_text" || first == "reasoning_only" {
				emit(ChatDelta{ReasoningContent: &before})
			}
			if first == "text" || first == "reasoning_then_text" {
				emit(ChatDelta{Content: &text})
			}
			if first == "tool" {
				emit(ChatDelta{ToolCalls: []ChatToolCall{{ID: "c", Function: ChatFunctionCall{Name: "lookup", Arguments: "{}"}}}})
			}
			emit(ChatDelta{ReasoningContent: &after})
			state.FinishReason = "stop"
			events = append(events, FinalizeChatCompletionsResponsesStream(state)...)
			terminal := events[len(events)-1].Response
			visible := ""
			for _, item := range terminal.Output {
				if item.Type == "reasoning" {
					visible += item.Summary[0].Text
				}
			}
			want := ""
			if first == "reasoning_then_text" {
				want = "BEFORE"
			}
			if first == "reasoning_only" {
				want = "BEFORELATE"
			}
			if visible != want {
				t.Fatalf("visible=%q want=%q", visible, want)
			}
			closed := false
			for _, e := range events {
				if e.Type == "response.reasoning_summary_text.done" {
					closed = true
				}
				if closed && e.Type == "response.reasoning_summary_text.delta" {
					t.Fatal("delta after done")
				}
			}
		})
	}
}

func TestM4TypedChatOutput(t *testing.T) {
	content := json.RawMessage(`[{"type":"thinking","thinking":"THINK"},{"type":"text","text":"ANSWER"},{"type":"thinking","thinking":{"text":"NEXT"}}]`)
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
	resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Content: content, ReasoningContent: "DUPLICATE"}, FinishReason: "stop"}}}
	out, err := ChatCompletionsResponseToResponsesWithBridgeOptions(resp, "m", nil, nil, false, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != 3 || out.Output[0].Summary[0].Text != "THINK" || out.Output[1].Content[0].Text != "ANSWER" || out.Output[2].Summary[0].Text != "NEXT" {
		t.Fatalf("output=%+v", out.Output)
	}
	anth, err := ChatCompletionsResponseToAnthropicWithBridgeOptions(resp, "m", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(anth.Content) != 3 || anth.Content[0].Thinking != "THINK" || anth.Content[1].Text != "ANSWER" || anth.Content[2].Thinking != "NEXT" {
		t.Fatalf("content=%+v", anth.Content)
	}
	raw := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"DUPLICATE","content":` + string(content) + `},"finish_reason":"stop"}]}`)
	state := NewChatCompletionsToResponsesStreamState("m")
	state.Bridge = opts.Bridge
	events := ChatCompletionsChunkToResponsesEventsWithBridgeOptions(raw, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(state)...)
	if state.Err() != nil {
		t.Fatal(state.Err())
	}
	terminal := events[len(events)-1].Response
	if len(terminal.Output) != 3 {
		t.Fatalf("terminal=%+v", terminal.Output)
	}
	added := map[int]string{}
	texts := map[string]string{}
	for _, e := range events {
		if e.Type == "response.output_item.added" {
			added[e.OutputIndex] = e.Item.ID
		}
		if e.Type == "response.reasoning_summary_text.delta" || e.Type == "response.output_text.delta" {
			texts[e.ItemID] += e.Delta
		}
		if e.Type == "response.output_item.done" && added[e.OutputIndex] != e.Item.ID {
			t.Fatal("done ID changed")
		}
	}
	for i, item := range terminal.Output {
		if item.ID != added[i] {
			t.Fatal("terminal ID/index changed")
		}
		want := ""
		if item.Type == "reasoning" {
			want = item.Summary[0].Text
		} else {
			want = item.Content[0].Text
		}
		if want != texts[item.ID] {
			t.Fatalf("terminal differs from deltas: %+v", item)
		}
	}
	as := NewChatCompletionsToAnthropicStreamState("m")
	as.Bridge = opts.Bridge
	ae := ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(raw, as)
	ae = append(ae, FinalizeChatCompletionsAnthropicStream(as)...)
	var got []string
	for _, e := range ae {
		if e.Delta != nil {
			if e.Delta.Type == "thinking_delta" {
				got = append(got, e.Delta.Thinking)
			}
			if e.Delta.Type == "text_delta" {
				got = append(got, e.Delta.Text)
			}
		}
	}
	if !reflect.DeepEqual(got, []string{"THINK", "ANSWER", "NEXT"}) {
		t.Fatalf("deltas=%v", got)
	}
	bad := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Content: json.RawMessage(`[{"type":"unknown","text":"x"}]`)}}}}
	if _, err := ChatCompletionsResponseToResponsesWithBridgeOptions(bad, "m", nil, nil, false, nil, opts); err == nil {
		t.Fatal("unknown output part accepted")
	}
	if _, err := ChatCompletionsResponseToAnthropicWithBridgeOptions(bad, "m", opts); err == nil {
		t.Fatal("unknown output part accepted")
	}
}

func TestM4WhitespaceThinking(t *testing.T) {
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
	out, err := ChatCompletionsResponseToAnthropicWithBridgeOptions(&ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ReasoningContent: " \n\t", Content: json.RawMessage(`"answer"`)}, FinishReason: "stop"}}}, "m", opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range out.Content {
		if b.Type == "thinking" {
			t.Fatal("blank buffered thinking")
		}
	}
	for _, texts := range [][]string{{" ", "\n"}, {" ", "word", " \t"}} {
		state := NewChatCompletionsToAnthropicStreamState("m")
		state.Bridge = opts.Bridge
		var events []AnthropicStreamEvent
		for _, text := range texts {
			value := text
			events = append(events, ChatCompletionsChunkToAnthropicEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ReasoningContent: &value}}}}, state)...)
		}
		answer := "answer"
		reason := "stop"
		events = append(events, ChatCompletionsChunkToAnthropicEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: &answer}, FinishReason: &reason}}}, state)...)
		events = append(events, FinalizeChatCompletionsAnthropicStream(state)...)
		thinking := ""
		starts := 0
		firstIndex := -1
		for _, e := range events {
			if e.Type == "content_block_start" && firstIndex < 0 {
				firstIndex = *e.Index
			}
			if e.ContentBlock != nil && e.ContentBlock.Type == "thinking" {
				starts++
			}
			if e.Delta != nil && e.Delta.Type == "thinking_delta" {
				thinking += e.Delta.Thinking
			}
		}
		want := ""
		wantStarts := 0
		if len(texts) == 3 {
			want = " word \t"
			wantStarts = 1
		}
		if thinking != want || starts != wantStarts || firstIndex != 0 {
			t.Fatalf("thinking=%q starts=%d first=%d", thinking, starts, firstIndex)
		}
	}
}

func TestM4RequestFieldContract(t *testing.T) {
	limit := 22
	req := &ResponsesRequest{Model: "probe", Input: json.RawMessage(`"hello"`), User: json.RawMessage(`"alice"`), Metadata: json.RawMessage(`{"project":"p"}`), Text: &ResponsesText{Verbosity: "low"}, MaxOutputTokens: &limit}
	out, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.User) != `"alice"` || string(out.Metadata) != `{"project":"p"}` || out.Verbosity != "low" || out.MaxCompletionTokens == nil || *out.MaxCompletionTokens != 22 {
		t.Fatalf("fields=%+v", out)
	}
	legacy, err := ResponsesToChatCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.User) != 0 || len(legacy.Metadata) != 0 || legacy.Verbosity != "" {
		t.Fatal("legacy fields changed")
	}
	out.Metadata[0] = 'x'
	if req.Metadata[0] != '{' {
		t.Fatal("metadata aliases input")
	}
}
