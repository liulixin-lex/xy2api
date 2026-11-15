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

func TestM4ReasoningHistoryFallback(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"type":"reasoning","id":"rs_fixed","summary":[],"content":[{"type":"reasoning_text","text":"A"},{"type":"reasoning_text","text":"B"}]}`, "AB"},
		{`{"type":"reasoning","id":"rs_fixed","summary":[{"type":"summary_text","text":"SUMMARY"}],"content":[{"type":"reasoning_text","text":"RAW"}]}`, "SUMMARY"},
		{`{"type":"reasoning","id":"rs_fixed","content":[{"type":"text","text":"NOT REASONING"}]}`, ""},
		{`{"type":"reasoning","id":"rs_fixed","encrypted_content":"opaque"}`, ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
			id, text, ok := ExtractResponsesReasoningItemWithBridgeOptions(json.RawMessage(tc.raw), opts)
			if !ok || id != "rs_fixed" || text != tc.want {
				t.Fatalf("id=%s text=%q ok=%v", id, text, ok)
			}
			input := json.RawMessage("[" + tc.raw + `,{"role":"assistant","content":"answer"}]`)
			out, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{Model: "m", Input: input}, &ResponsesToChatOptions{Bridge: opts.Bridge, ReasoningContentByID: func(string) string { return "CACHE" }})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = "CACHE"
			}
			if out.Messages[0].ReasoningContent != want {
				t.Fatalf("history=%+v", out.Messages)
			}
		})
	}
	raw := json.RawMessage(`{"type":"reasoning","content":[{"type":"text","text":"legacy"}]}`)
	_, text, ok := ExtractResponsesReasoningItemWithBridgeOptions(raw, ConversionOptions{})
	if !ok || text != "legacy" {
		t.Fatal("zero options changed")
	}
}

func TestM4InvalidToolArguments(t *testing.T) {
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
	for _, args := range []string{`{broken`, `{"q":"unterminated`} {
		resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{{ID: "c", Function: ChatFunctionCall{Name: "lookup", Arguments: args}}}}, FinishReason: "tool_calls"}}}
		if _, err := ChatCompletionsResponseToResponsesWithBridgeOptions(resp, "m", nil, nil, false, nil, opts); err == nil {
			t.Fatal("bad generated call silently omitted")
		}
		if _, err := ChatCompletionsResponseToAnthropicWithBridgeOptions(resp, "m", opts); err == nil {
			t.Fatal("bad generated call sanitized")
		}
		state := NewChatCompletionsToResponsesStreamState("m")
		state.Bridge = opts.Bridge
		reason := "tool_calls"
		ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: resp.Choices[0].Message.ToolCalls}, FinishReason: &reason}}}, state)
		if events := FinalizeChatCompletionsResponsesStream(state); state.Err() == nil || len(events) != 0 {
			t.Fatal("bad generated stream completed")
		}
	}
	for _, tc := range []struct {
		kind, args string
		bad        bool
	}{{"function", "{}", false}, {"function", "[]", false}, {"custom", "free text", false}, {"custom", "", false}, {"tool_search", `{"query":"q"}`, false}, {"tool_search", "[]", true}} {
		if err := validateBridgeToolArguments(tc.kind, tc.args, false); (err != nil) != tc.bad {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	state := NewChatCompletionsToResponsesStreamState("m")
	state.Bridge = opts.Bridge
	ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: []ChatToolCall{{ID: "c", Function: ChatFunctionCall{Name: "lookup", Arguments: "{"}}}}}}}, state)
	if len(FinalizeChatCompletionsResponsesStream(state)) != 0 || state.Err() == nil {
		t.Fatal("EOF became successful empty arguments")
	}
}

func TestM4LengthToolGate(t *testing.T) {
	for _, kind := range []string{"function", "custom", "tool_search"} {
		for _, args := range []string{`{}`, ``, `{broken`} {
			t.Run(kind+args, func(t *testing.T) {
				bridge := &BridgeOptions{Enabled: true}
				name := "lookup"
				custom := map[string]bool(nil)
				search := false
				if kind == "custom" {
					name = "exec"
					custom = map[string]bool{name: true}
					bridge.ToolNames = &BridgeToolNameMap{Reverse: map[string]ToolNameIdentity{name: {Kind: "custom", Name: name}}}
				}
				if kind == "tool_search" {
					name = toolSearchProxyName
					search = true
				}
				call := ChatToolCall{ID: "c", Function: ChatFunctionCall{Name: name, Arguments: args}}
				response := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Content: json.RawMessage(`"partial"`), ToolCalls: []ChatToolCall{call}}, FinishReason: "length"}}, Usage: &ChatUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}
				out, err := ChatCompletionsResponseToResponsesWithBridgeOptions(response, "m", custom, nil, search, nil, ConversionOptions{Bridge: bridge})
				if err != nil {
					t.Fatal(err)
				}
				if out.Status != "incomplete" || len(out.Output) != 2 || out.Output[1].Status != "incomplete" || out.Usage.TotalTokens != 7 {
					t.Fatalf("buffered=%+v", out)
				}
				state := NewChatCompletionsToResponsesStreamState("m")
				state.Bridge = bridge
				state.CustomTools = custom
				state.ToolSearchDeclared = search
				reason := "length"
				events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: []ChatToolCall{call}}, FinishReason: &reason}}, Usage: response.Usage}, state)
				events = append(events, FinalizeChatCompletionsResponsesStream(state)...)
				if state.Err() != nil {
					t.Fatal(state.Err())
				}
				for _, e := range events {
					if e.Type == "response.function_call_arguments.done" || e.Type == "response.custom_tool_call_input.done" || (e.Type == "response.output_item.done" && e.Item.Type != "message") {
						t.Fatalf("truncated executable completion: %+v", e)
					}
				}
				terminal := events[len(events)-1].Response
				if terminal.Status != "incomplete" || terminal.Output[0].Status != "incomplete" || terminal.Usage.TotalTokens != 7 {
					t.Fatalf("terminal=%+v", terminal)
				}
			})
		}
	}
}

func TestM4ChatTerminalEventType(t *testing.T) {
	for _, reason := range []string{"stop", "length"} {
		state := NewChatCompletionsToResponsesStreamState("m")
		state.Bridge = &BridgeOptions{Enabled: true}
		text := "answer"
		events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{ID: "fixed", Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: &text}, FinishReason: &reason}}, Usage: &ChatUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}, state)
		events = append(events, FinalizeChatCompletionsResponsesStream(state)...)
		last := events[len(events)-1]
		status := "completed"
		if reason == "length" {
			status = "incomplete"
		}
		if last.Type != "response."+status || last.Response.Status != status || last.Response.ID != "fixed" || last.Response.Usage.TotalTokens != 7 || last.Response.Output[0].Content[0].Text != "answer" {
			t.Fatalf("terminal=%+v", last)
		}
		if reason == "length" && last.Response.IncompleteDetails.Reason != "max_output_tokens" {
			t.Fatal("length reason lost")
		}
		if len(FinalizeChatCompletionsResponsesStream(state)) != 0 {
			t.Fatal("duplicate terminal")
		}
	}
}

func TestM4TerminalReasonProjection(t *testing.T) {
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
	for _, tc := range []struct {
		source, reason, status, detail string
		bad                            bool
	}{
		{"chat", "stop", "completed", "", false}, {"chat", "length", "incomplete", "max_output_tokens", false},
		{"chat", "content_filter", "incomplete", "content_filter", false}, {"chat", "unknown", "completed", "", false}, {"chat", "", "completed", "", false},
		{"anthropic", "max_tokens", "incomplete", "max_output_tokens", false}, {"anthropic", "model_context_window_exceeded", "incomplete", "max_output_tokens", false},
		{"anthropic", "refusal", "completed", "", false}, {"anthropic", "end_turn", "completed", "", false}, {"anthropic", "pause_turn", "", "", true}, {"anthropic", "unknown", "completed", "", false}, {"anthropic", "", "completed", "", false},
	} {
		t.Run(tc.source+"/"+tc.reason, func(t *testing.T) {
			var buffered *ResponsesResponse
			var err error
			var events []ResponsesStreamEvent
			if tc.source == "chat" {
				resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: json.RawMessage(`"kept"`)}, FinishReason: tc.reason}}}
				buffered, err = ChatCompletionsResponseToResponsesWithBridgeOptions(resp, "m", nil, nil, false, nil, opts)
				st := NewChatCompletionsToResponsesStreamState("m")
				st.Bridge = opts.Bridge
				st.FinishReason = tc.reason
				text := "kept"
				ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: &text}}}}, st)
				events = FinalizeChatCompletionsResponsesStream(st)
				if (st.Err() != nil) != tc.bad {
					t.Fatalf("stream error: %v", st.Err())
				}
				anth, e := ChatCompletionsResponseToAnthropicWithBridgeOptions(resp, "m", opts)
				as := NewChatCompletionsToAnthropicStreamState("m")
				as.Bridge = opts.Bridge
				as.FinishReason = tc.reason
				ChatCompletionsChunkToAnthropicEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: &text}}}}, as)
				ae := FinalizeChatCompletionsAnthropicStream(as)
				if (e != nil) != tc.bad || (as.Err() != nil) != tc.bad {
					t.Fatalf("Anthropic errors: %v / %v", e, as.Err())
				}
				if !tc.bad && (len(ae) < 2 || AnthropicStopReasonString(anth.StopReason) != ae[len(ae)-2].Delta.StopReason) {
					t.Fatalf("Anthropic mismatch: %#v / %#v", anth, ae)
				}
			} else {
				buffered, err = AnthropicToResponsesResponseWithBridgeOptions(&AnthropicResponse{StopReason: AnthropicStopReasonPtr(tc.reason), Content: []AnthropicContentBlock{{Type: "text", Text: "kept"}}}, opts)
				st := NewAnthropicEventToResponsesState()
				st.Bridge = opts.Bridge
				st.CreatedSent = true
				st.StopReason = tc.reason
				st.Outputs = []ResponsesOutput{{Type: "message", Content: []ResponsesContentPart{{Type: "output_text", Text: "kept"}}}}
				events = AnthropicEventToResponsesEventsWithBridgeOptions(&AnthropicStreamEvent{Type: "message_stop"}, st, opts)
				if (st.Err() != nil) != tc.bad {
					t.Fatalf("stream error: %v", st.Err())
				}
			}
			if (err != nil) != tc.bad {
				t.Fatalf("buffer error: %v", err)
			}
			if tc.bad {
				for _, e := range events {
					if strings.HasPrefix(e.Type, "response.completed") {
						t.Fatal("error completed")
					}
				}
				return
			}
			if buffered.Status != tc.status || len(events) == 0 {
				t.Fatalf("status: %#v / %#v", buffered, events)
			}
			terminal := events[len(events)-1]
			if terminal.Type != "response."+tc.status || terminal.Response.Status != tc.status {
				t.Fatalf("terminal %#v", terminal)
			}
			for _, out := range []*ResponsesResponse{buffered, terminal.Response} {
				detail := ""
				if out.IncompleteDetails != nil {
					detail = out.IncompleteDetails.Reason
				}
				if detail != tc.detail {
					t.Fatalf("detail=%q", detail)
				}
				raw, _ := json.Marshal(out.Output)
				if !strings.Contains(string(raw), "kept") {
					t.Fatalf("lost text: %s", raw)
				}
			}
		})
	}
	t.Run("projection_error_latched", func(t *testing.T) {
		st := NewAnthropicEventToResponsesState()
		st.CreatedSent = true
		st.Bridge = opts.Bridge
		st.StopReason = "pause_turn"
		AnthropicEventToResponsesEventsWithBridgeOptions(&AnthropicStreamEvent{Type: "message_stop"}, st, opts)
		first := st.Err()
		if first == nil {
			t.Fatal("missing pause error")
		}
		st.StopReason = "end_turn"
		if len(AnthropicEventToResponsesEventsWithBridgeOptions(&AnthropicStreamEvent{Type: "message_stop"}, st, opts)) != 0 || len(FinalizeAnthropicResponsesStreamWithBridgeOptions(st, opts)) != 0 || st.Err() != first {
			t.Fatal("projection error did not remain terminal")
		}
	})
	t.Run("refusal", func(t *testing.T) {
		resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Refusal: "declined"}, FinishReason: "stop"}}}
		out, err := ChatCompletionsResponseToResponsesWithBridgeOptions(resp, "m", nil, nil, false, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"type":"refusal","refusal":"declined"`) {
			t.Fatalf("lost refusal: %s", raw)
		}
		st := NewChatCompletionsToResponsesStreamState("m")
		st.Bridge = opts.Bridge
		events := ChatCompletionsChunkToResponsesEventsWithBridgeOptions([]byte(`{"choices":[{"index":0,"delta":{"refusal":"declined"},"finish_reason":"stop"}]}`), st)
		events = append(events, FinalizeChatCompletionsResponsesStream(st)...)
		delta, done := 0, 0
		for _, event := range events {
			if event.Type == "response.refusal.delta" && event.Delta == "declined" {
				delta++
			}
			if event.Type == "response.refusal.done" && event.Refusal == "declined" {
				done++
			}
		}
		if st.Err() != nil || delta != 1 || done != 1 {
			t.Fatalf("refusal lifecycle: %v %#v", st.Err(), events)
		}
		raw, _ = json.Marshal(events[len(events)-1].Response)
		if !strings.Contains(string(raw), `"type":"refusal","refusal":"declined"`) {
			t.Fatalf("lost terminal refusal: %s", raw)
		}
		a, e := ChatCompletionsResponseToAnthropicWithBridgeOptions(resp, "m", opts)
		ast := NewChatCompletionsToAnthropicStreamState("m")
		ast.Bridge = opts.Bridge
		ae := ChatCompletionsChunkToAnthropicEventsWithBridgeOptions([]byte(`{"choices":[{"index":0,"delta":{"refusal":"declined"},"finish_reason":"stop"}]}`), ast)
		ae = append(ae, FinalizeChatCompletionsAnthropicStream(ast)...)
		if e != nil || ast.Err() != nil || AnthropicStopReasonString(a.StopReason) != "refusal" || ae[len(ae)-2].Delta.StopReason != "refusal" {
			t.Fatalf("Anthropic refusal: %#v %v %#v", a, e, ae)
		}
	})
}

func TestM4AnthropicRequestHandoffs(t *testing.T) {
	t.Run("claude52", func(t *testing.T) {
		for _, model := range []string{"claude-fable-5-2", "claude-mythos-5-2", "claude-sonnet-5-5", "unknown"} {
			for _, signed := range []bool{true, false} {
				envelope := "opaque-openai-ciphertext"
				if signed {
					envelope = encodeAnthropicThinking(AnthropicContentBlock{Type: "thinking", Thinking: "signed text", Signature: "sig"})
				}
				input, _ := json.Marshal([]ResponsesInputItem{{Type: "reasoning", EncryptedContent: envelope}, {Role: "user", Content: json.RawMessage(`"hi"`)}})
				req := &ResponsesRequest{Model: model, Input: input}
				out, err := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(out.Messages)
				want := signed && model != "unknown"
				if strings.Contains(string(raw), "signed text") != want {
					t.Fatalf("%s signed=%v: %s", model, signed, raw)
				}
				old, err := ResponsesToAnthropicRequest(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ = json.Marshal(old.Messages)
				if strings.Contains(string(raw), "signed text") != (signed && model == "claude-sonnet-5-5") {
					t.Fatalf("legacy changed: %s", raw)
				}
			}
		}
	})
}

func TestM4AnthropicMediaHandoffs(t *testing.T) {
	input := json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png","detail":"high"}]},{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_file","file_url":"https://example.test/a.pdf","detail":"low"}]}]`)
	req := &ResponsesRequest{Model: "m", Input: input}
	original := string(input)
	var notices []ConversionNotice
	out, err := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out.Messages)
	if !strings.Contains(string(raw), "a.png") || !strings.Contains(string(raw), "a.pdf") || strings.Contains(string(raw), "detail") || string(req.Input) != original {
		t.Fatalf("media: %s", raw)
	}
	want := []ConversionNotice{{Field: "input[0].content[0].detail", Action: "omit"}, {Field: "input[2].output[0].detail", Action: "omit"}}
	if !reflect.DeepEqual(notices, want) {
		t.Fatalf("notices=%#v", notices)
	}
	if _, err := ResponsesToAnthropicRequest(req); err == nil {
		t.Fatal("legacy detail rejection changed")
	}
	req.Input = json.RawMessage(`[{"role":"user","content":[{"type":"input_file","file_id":"provider"}]}]`)
	if _, err := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{}); err == nil || !strings.Contains(err.Error(), "input[0].content[0].file_id") {
		t.Fatalf("field path: %v", err)
	}
	req.Input = json.RawMessage(`[{"type":"reasoning","content":[{"type":"reasoning_text","text":"private"}],"encrypted_content":"opaque"},{"role":"assistant","content":[{"type":"output_text","text":"answer"}]}]`)
	old, e := ResponsesToAnthropicRequest(req)
	if e != nil {
		t.Fatal(e)
	}
	current, e := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{})
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(current)
	if string(a) != string(b) {
		t.Fatalf("zero options reasoning changed: %s / %s", a, b)
	}
}

func TestM4ToolHistoryRounds(t *testing.T) {
	var fixtures []struct {
		Name                      string
		Input                     json.RawMessage
		Chat, Anthropic           json.RawMessage
		ChatError, AnthropicError string
	}
	raw, err := os.ReadFile("testdata/m4-history-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if fixture.Name == "id_reused_across_rounds" || len(fixture.Input) == 0 || string(fixture.Input) == "null" {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			req := &ResponsesRequest{Model: "probe", Stream: true, Input: fixture.Input}
			chat, e := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}})
			m4CheckError(t, e, fixture.ChatError)
			if e == nil {
				out, _ := json.Marshal(chat)
				m4CheckJSONBytes(t, out, fixture.Chat)
			}
			anth, e := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{})
			m4CheckError(t, e, fixture.AnthropicError)
			if e == nil {
				out, _ := json.Marshal(anth)
				m4CheckJSONBytes(t, out, fixture.Anthropic)
			}
		})
	}
	for _, tc := range []struct {
		name, input string
		want        []string
	}{
		{"two_rounds", `[{"type":"function_call","call_id":"call_same","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_same","output":"first"},{"role":"user","content":"next"},{"type":"function_call","call_id":"call_same","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_same","output":"second"}]`, []string{"first", "second"}},
		{"interleaved", `[{"type":"function_call","call_id":"call_a","name":"f","arguments":"{}"},{"type":"function_call","call_id":"call_b","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":"A"},{"type":"function_call","call_id":"call_c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_b","output":"B"},{"type":"function_call_output","call_id":"call_c","output":"C"}]`, []string{"A", "B", "C"}},
		{"same_round", `[{"type":"function_call","call_id":"call_a","name":"f","arguments":"{}"},{"type":"function_call","call_id":"call_a","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":"first"},{"type":"function_call_output","call_id":"call_a","output":"second"}]`, []string{"second", "second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &ResponsesRequest{Model: "probe", Input: json.RawMessage(tc.input)}
			chat, e := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}})
			if e != nil {
				t.Fatal(e)
			}
			var got []string
			for _, m := range chat.Messages {
				if m.Role == "tool" {
					var text string
					_ = json.Unmarshal(m.Content, &text)
					got = append(got, text)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Chat results: %#v", got)
			}
			anth, e := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{})
			if e != nil {
				t.Fatal(e)
			}
			got = nil
			for _, m := range anth.Messages {
				for _, b := range parseContentBlocks(m.Content) {
					if b.Type == "tool_result" {
						var text string
						_ = json.Unmarshal(b.Content, &text)
						got = append(got, text)
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Anthropic results: %#v", got)
			}
			for _, out := range []any{chat, anth} {
				raw, _ := json.Marshal(out)
				if strings.Contains(string(raw), "call_m4_occurrence_") {
					t.Fatalf("private key leaked: %s", raw)
				}
			}
		})
	}
	t.Run("media_occurrence", func(t *testing.T) {
		req := &ResponsesRequest{Model: "probe", Input: json.RawMessage(`[{"type":"function_call","call_id":"call_x","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_x","output":[{"type":"input_image","image_url":"https://example.test/first.png"}]},{"type":"function_call","call_id":"call_x","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_x","output":[{"type":"input_image","image_url":"https://example.test/second.png"}]}]`)}
		out, e := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(out)
		text := string(raw)
		if strings.Count(text, "first.png") != 1 || strings.Count(text, "second.png") != 1 || strings.Index(text, "first.png") > strings.Index(text, "second.png") || strings.Contains(text, "call_m4_occurrence") {
			t.Fatalf("media pairing: %s", raw)
		}
	})
}

func TestM4UsagePresenceHandoffs(t *testing.T) {
	for _, raw := range []string{`{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}`, `{"prompt_tokens":5}`, `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, `{"prompt_tokens":5,"completion_tokens":2,"total_tokens":0}`} {
		var usage ChatUsage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			t.Fatal(err)
		}
		out := ChatUsageToResponsesUsage(&usage)
		if !out.UsagePresenceKnown || out.InputTokensPresent != usage.PromptTokensPresent || out.OutputTokensPresent != usage.CompletionTokensPresent || out.TotalTokensPresent != usage.TotalTokensPresent {
			t.Fatalf("presence: %#v / %#v", usage, out)
		}
		legacy := usage
		legacy.UsagePresenceKnown = false
		legacy.PromptTokensPresent = false
		legacy.CompletionTokensPresent = false
		legacy.TotalTokensPresent = false
		a, _ := json.Marshal(ChatUsageToResponsesUsage(&legacy))
		b, _ := json.Marshal(out)
		if string(a) != string(b) {
			t.Fatalf("wire changed: %s / %s", a, b)
		}
	}
	unknown := ChatUsageToResponsesUsage(&ChatUsage{PromptTokens: 5, CompletionTokens: 2})
	if unknown.UsagePresenceKnown || unknown.TotalTokens != 7 {
		t.Fatalf("unknown changed: %#v", unknown)
	}
	for _, raw := range []string{`{"input_tokens":5,"output_tokens":2}`, `{"input_tokens":5}`, `{"input_tokens":0,"output_tokens":0}`} {
		var usage AnthropicUsage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			t.Fatal(err)
		}
		resp := &AnthropicResponse{ID: "fixed", StopReason: AnthropicStopReasonPtr("end_turn"), Usage: usage}
		old := AnthropicToResponsesResponse(resp)
		out, e := AnthropicToResponsesResponseWithBridgeOptions(resp, ConversionOptions{Bridge: &BridgeOptions{Enabled: true}})
		if e != nil {
			t.Fatal(e)
		}
		if !out.Usage.UsagePresenceKnown || out.Usage.InputTokensPresent != usage.InputTokensPresent || out.Usage.OutputTokensPresent != usage.OutputTokensPresent || out.Usage.TotalTokensPresent {
			t.Fatalf("presence: %#v", out.Usage)
		}
		a, _ := json.Marshal(old.Usage)
		b, _ := json.Marshal(out.Usage)
		if string(a) != string(b) {
			t.Fatalf("Anthropic wire changed: %s / %s", a, b)
		}
	}
	state := NewAnthropicEventToResponsesState()
	state.Bridge = &BridgeOptions{Enabled: true}
	state.CreatedSent = true
	state.StopReason = "end_turn"
	state.InputTokens = 5
	events := FinalizeAnthropicResponsesStreamWithBridgeOptions(state, ConversionOptions{Bridge: state.Bridge})
	usage := events[len(events)-1].Response.Usage
	if usage.UsagePresenceKnown || usage.InputTokens != 5 || usage.OutputTokens != 0 || usage.TotalTokens != 5 {
		t.Fatalf("E7/E8 stream usage: %#v", usage)
	}
}

func TestM4ToolChoiceSubset(t *testing.T) {
	for _, allow := range []string{`[{"type":"function","namespace":"ns","name":"f"},{"type":"function","namespace":"ns","name":"f"},{"type":"function","name":"missing"}]`, `[]`} {
		req := &ResponsesRequest{Model: "claude-sonnet-4-6", Tools: []ResponsesTool{{Type: "namespace", Name: "ns", Tools: []ResponsesTool{{Type: "function", Name: "f", Parameters: json.RawMessage(`{"type":"object"}`)}}}, {Type: "function", Name: "excluded", Parameters: json.RawMessage(`false`)}}, ToolChoice: json.RawMessage(`{"type":"allowed_tools","mode":"auto","tools":` + allow + `}`), Input: json.RawMessage(`[{"type":"function_call","call_id":"call_old","name":"historical","arguments":"{}"},{"type":"function_call_output","call_id":"call_old","output":"kept"}]`)}
		var notices []ConversionNotice
		bridge := &BridgeOptions{Enabled: true}
		out, err := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{Bridge: bridge, Record: func(n ConversionNotice) { notices = append(notices, n) }})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if allow == "[]" {
			want = 0
		}
		if len(out.Tools) != want {
			t.Fatalf("tools=%#v", out.Tools)
		}
		if want == 1 {
			if out.Tools[0].Name != bridge.ToolNames.Forward[ToolNameIdentity{Kind: "function", Namespace: "ns", Name: "f"}] || !reflect.DeepEqual(notices, []ConversionNotice{{Field: "tool_choice.tools[2]", Action: "omit"}}) {
				t.Fatalf("selection: %#v / %#v", out.Tools, notices)
			}
		} else if string(out.ToolChoice) != `{"type":"none"}` {
			t.Fatalf("empty choice=%s", out.ToolChoice)
		}
		raw, _ := json.Marshal(out.Messages)
		if !strings.Contains(string(raw), "historical") || !strings.Contains(string(raw), "kept") {
			t.Fatalf("history lost: %s", raw)
		}
		chat, e := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}})
		if e != nil {
			t.Fatal(e)
		}
		if len(chat.Tools) != want || (want == 0 && string(chat.ToolChoice) != `"none"`) {
			t.Fatalf("Chat subset: %#v", chat)
		}
	}
	req := &ResponsesRequest{Model: "claude-sonnet-4-6", Tools: []ResponsesTool{{Type: "namespace", Name: "ns", Tools: []ResponsesTool{{Type: "custom", Name: "exec"}}}}, ToolChoice: json.RawMessage(`{"type":"custom","namespace":"ns","name":"exec"}`), Input: json.RawMessage(`[{"type":"custom_tool_call","call_id":"call_x","namespace":"ns","name":"exec","input":"echo ok"},{"type":"custom_tool_call_output","call_id":"call_x","output":"done"}]`)}
	bridge := &BridgeOptions{Enabled: true}
	out, err := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{Bridge: bridge})
	if err != nil {
		t.Fatal(err)
	}
	alias := bridge.ToolNames.Forward[ToolNameIdentity{Kind: "custom", Namespace: "ns", Name: "exec"}]
	if len(out.Tools) != 1 || out.Tools[0].Name != alias || !strings.Contains(string(out.ToolChoice), alias) {
		t.Fatalf("custom choice: %#v", out)
	}
	raw, _ := json.Marshal(out.Messages)
	if !strings.Contains(string(raw), "echo ok") || !strings.Contains(string(raw), alias) {
		t.Fatalf("custom history: %s", raw)
	}
	required := &ResponsesRequest{Model: "m", Tools: []ResponsesTool{{Type: "function", Name: "f", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolChoice: json.RawMessage(`{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"f"}]}`)}
	requiredOut, e := ResponsesToChatCompletionsRequestWithOptions(required, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}})
	if e != nil || string(requiredOut.ToolChoice) != `"required"` {
		t.Fatalf("required mode: %#v %v", requiredOut, e)
	}
	// The private original Chat source remains a copied policy input after aliasing.
	chatReq := &ChatCompletionsRequest{Model: "claude-sonnet-4-6", Tools: []ChatTool{{Type: "function", Function: &ChatFunction{Name: "工具", Parameters: json.RawMessage(`{"type":"object"}`)}}}, Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	intermediate, err := ChatCompletionsToResponses(chatReq)
	if err != nil {
		t.Fatal(err)
	}
	bridge = &BridgeOptions{Enabled: true}
	out, err = ResponsesToAnthropicRequestWithOptions(intermediate, ConversionOptions{Bridge: bridge})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tools[0].Name == "工具" || chatReq.Tools[0].Function.Name != "工具" {
		t.Fatal("Chat source alias/copy failed")
	}
}

func TestM4UnknownTerminalMapping(t *testing.T) {
	var notices []ConversionNotice
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}, Record: func(n ConversionNotice) { notices = append(notices, n) }}
	check := func(field string) {
		t.Helper()
		if !reflect.DeepEqual(notices, []ConversionNotice{{Field: field, Action: "map"}}) {
			t.Fatalf("notices=%#v", notices)
		}
		notices = nil
	}
	resp := &ChatCompletionsResponse{Choices: []ChatChoice{{FinishReason: "vendor_done", Message: ChatMessage{Content: json.RawMessage(`"kept"`)}}}}
	out, err := ChatCompletionsResponseToResponsesWithBridgeOptions(resp, "m", nil, nil, false, nil, opts)
	if err != nil || out.Status != "completed" {
		t.Fatalf("unknown: %#v %v", out, err)
	}
	check("finish_reason")
	anth, err := ChatCompletionsResponseToAnthropicWithBridgeOptions(resp, "m", opts)
	if err != nil || AnthropicStopReasonString(anth.StopReason) != "end_turn" {
		t.Fatalf("unknown: %#v %v", anth, err)
	}
	check("finish_reason")
	for _, tools := range []bool{false, true} {
		st := NewChatCompletionsToResponsesStreamState("m")
		st.Bridge = opts.Bridge
		st.SetConversionRecorder(opts.Record)
		st.FinishReason = "vendor_done"
		ast := NewChatCompletionsToAnthropicStreamState("m")
		ast.Bridge = opts.Bridge
		ast.SetConversionRecorder(opts.Record)
		ast.FinishReason = "vendor_done"
		if tools {
			var chunk ChatCompletionsChunk
			_ = json.Unmarshal([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"f","arguments":"{}"}}]}}]}`), &chunk)
			ChatCompletionsChunkToResponsesEvents(&chunk, st)
			ChatCompletionsChunkToAnthropicEvents(&chunk, ast)
		}
		events := FinalizeChatCompletionsResponsesStream(st)
		if st.Err() != nil || len(events) == 0 || events[len(events)-1].Type != "response.completed" {
			t.Fatalf("unknown stream: %v %#v", st.Err(), events)
		}
		if len(FinalizeChatCompletionsResponsesStream(st)) != 0 {
			t.Fatal("duplicate terminal")
		}
		check("finish_reason")
		ae := FinalizeChatCompletionsAnthropicStream(ast)
		if ast.Err() != nil || len(ae) < 2 {
			t.Fatalf("unknown stream: %v %#v", ast.Err(), ae)
		}
		want := "end_turn"
		if tools {
			want = "tool_use"
		}
		if ae[len(ae)-2].Delta.StopReason != want {
			t.Fatal("wrong tool projection")
		}
		check("finish_reason")
	}
	_, err = AnthropicToResponsesResponseWithBridgeOptions(&AnthropicResponse{StopReason: AnthropicStopReasonPtr("vendor_done")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	check("stop_reason")
	state := NewAnthropicEventToResponsesState()
	AnthropicEventToResponsesEventsWithBridgeOptions(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "fixed"}}, state, opts)
	state.StopReason = "vendor_done"
	events := AnthropicEventToResponsesEventsWithBridgeOptions(&AnthropicStreamEvent{Type: "message_stop"}, state, opts)
	if state.Err() != nil || len(events) != 1 || events[0].Type != "response.completed" {
		t.Fatalf("unknown Anthropic: %v %#v", state.Err(), events)
	}
	check("stop_reason")
}

func TestM4AnthropicToChatHistoryUnchanged(t *testing.T) {
	var req AnthropicRequest
	input := []byte(`{"model":"probe","cache_control":{"type":"ephemeral"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"工具","input":{},"cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"one"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"工具","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"two"}]}]}`)
	if err := json.Unmarshal(input, &req); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(req.Messages)
	out, err := AnthropicToChatCompletionsRequest(&req)
	if err != nil {
		t.Fatal(err)
	}
	if out.CacheControl != nil {
		t.Fatal("unexpected top-level cache control")
	}
	req.CacheControl = nil
	control, err := AnthropicToChatCompletionsRequest(&req)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(out)
	b, _ := json.Marshal(control)
	if string(a) != string(b) {
		t.Fatalf("top-level cache marker affected projection: %s / %s", a, b)
	}
	if strings.Count(string(a), `"content":"two"`) != 2 || strings.Contains(string(a), "one") {
		t.Fatalf("legacy pairing changed: %s", a)
	}
	after, _ := json.Marshal(req.Messages)
	if string(before) != string(after) {
		t.Fatal("input block declarations changed")
	}
}
