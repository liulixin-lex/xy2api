//go:build unit

package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func m4ReviewObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestM4ReviewRawToolRestoration(t *testing.T) {
	for _, kind := range []string{"tool_search", "custom"} {
		request := `{"model":"m","input":"hi","tools":[{"type":"tool_search","execution":"client"},{"type":"custom","name":"exec"},{"type":"function","name":"f","parameters":{"type":"object"}}]}`
		old, _, err := AdaptResponsesClientTools(m4ReviewObject(t, request))
		if err != nil {
			t.Fatal(err)
		}
		revised, _, err := AdaptResponsesClientToolsWithBridgeOptions(m4ReviewObject(t, request), ConversionOptions{Bridge: &BridgeOptions{Enabled: true}})
		if err != nil {
			t.Fatal(err)
		}
		name, args, prefix := "tool_search", `{"query":"x"}`, "tsc_"
		if kind == "custom" {
			name, args, prefix = "exec", `{"input":"echo ok"}`, "ctc_"
		}
		payload, _ := json.Marshal(map[string]any{"id": "resp_1", "object": "response", "status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": name, "arguments": args, "status": "completed"}}})
		expected, _, err := RestoreResponsesClientToolPayload(payload, old)
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := RestoreResponsesClientToolPayload(payload, revised)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(expected) || !strings.Contains(string(got), `"id":"`+prefix+`1"`) {
			t.Fatalf("%s restored=%s expected=%s", kind, got, expected)
		}
		if kind == "tool_search" && !strings.Contains(string(got), `"arguments":{"query":"x"}`) {
			t.Fatalf("search arguments not object: %s", got)
		}
		terminal := []byte(`{"type":"response.completed","sequence_number":3,"response":` + string(payload) + `}`)
		a, _, err := NewResponsesClientToolStreamRestorer(old).RestoreEvent(terminal)
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := NewResponsesClientToolStreamRestorer(revised).RestoreEvent(terminal)
		if err != nil {
			t.Fatal(err)
		}
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		if string(aj) != string(bj) {
			t.Fatalf("terminal differs: %s / %s", aj, bj)
		}
	}
}

func TestM4ReviewDiscoveredToolNames(t *testing.T) {
	for _, tc := range []struct{ name, namespace, tool string }{
		{"namespace", "collaboration", "spawn_agent"},
		{"long_namespace", "mcp__codex_apps__microsoft_sharepoint", "list_site_drive_items_recursively"},
		{"long_function", "", strings.Repeat("x", 70)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			discovered := map[string]any{"type": "function", "name": tc.tool, "parameters": map[string]any{"type": "object"}}
			if tc.namespace != "" {
				discovered = map[string]any{"type": "namespace", "name": tc.namespace, "tools": []any{discovered}}
			}
			obj := m4ReviewObject(t, `{"model":"m","tools":[{"type":"tool_search"},{"type":"function","name":"inspect","parameters":{"type":"object"}}],"input":[{"type":"tool_search_call","call_id":"search_1","arguments":{"query":"workspace"}},{"type":"tool_search_output","call_id":"search_1","status":"completed","execution":"client","tools":[{"type":"function","name":"inspect","parameters":{"type":"object"}},{"type":"custom","name":"exec"}]}]}`)
			output := obj["input"].([]any)[1].(map[string]any)
			output["tools"] = append(output["tools"].([]any), discovered)
			raw, _ := json.Marshal(obj)
			var req ResponsesRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			old, err := ResponsesToChatCompletionsRequest(&req)
			if err != nil || len(old.Tools) != 4 {
				t.Fatalf("baseline: %#v %v", old, err)
			}
			bridge := &BridgeOptions{Enabled: true}
			chat, err := ResponsesToChatCompletionsRequestWithOptions(&req, &ResponsesToChatOptions{Bridge: bridge})
			if err != nil {
				t.Fatal(err)
			}
			id := ToolNameIdentity{Kind: "function", Namespace: tc.namespace, Name: tc.tool}
			alias := bridge.ToolNames.Forward[id]
			if len(chat.Tools) != 4 || chat.Tools[3].Function.Name != alias || !validBridgeToolName(alias, 64) {
				t.Fatalf("Chat declarations=%#v alias=%q", chat.Tools, alias)
			}
			rawBridge := &BridgeOptions{Enabled: true}
			value := m4ReviewObject(t, string(raw))
			mapping, _, err := AdaptResponsesClientToolsWithBridgeOptions(value, ConversionOptions{Bridge: rawBridge})
			if err != nil {
				t.Fatal(err)
			}
			tools := value["tools"].([]any)
			alias = rawBridge.ToolNames.Forward[id]
			if len(tools) != 4 || tools[3].(map[string]any)["name"] != alias || !validBridgeToolName(alias, 64) {
				t.Fatalf("Anthropic declarations=%#v alias=%q", tools, alias)
			}
			payload, _ := json.Marshal(map[string]any{"output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "c1", "name": alias, "arguments": "{}"}}})
			restored, _, err := RestoreResponsesClientToolPayload(payload, mapping)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct{ Output []ResponsesOutput }
			if err = json.Unmarshal(restored, &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Output) != 1 || parsed.Output[0].Name != tc.tool || parsed.Output[0].Namespace != tc.namespace {
				t.Fatalf("restored=%s", restored)
			}
			before, _ := json.Marshal(value)
			if _, _, err = AdaptResponsesClientToolsWithBridgeOptions(value, ConversionOptions{Bridge: rawBridge}); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(value)
			if string(before) != string(after) {
				t.Fatalf("second adaptation changed request: %s / %s", before, after)
			}
		})
	}
}

func TestM4ReviewAuthenticatedChatFinish(t *testing.T) {
	for _, tailUsage := range []bool{false, true} {
		st := NewChatCompletionsToResponsesStreamState("m")
		st.Bridge = &BridgeOptions{Enabled: true}
		ast := NewChatCompletionsToAnthropicStreamState("m")
		ast.Bridge = st.Bridge
		raw := []byte(`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`)
		ChatCompletionsChunkToResponsesEventsWithBridgeOptions(raw, st)
		ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(raw, ast)
		if tailUsage {
			tail := []byte(`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
			ChatCompletionsChunkToResponsesEventsWithBridgeOptions(tail, st)
			ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(tail, ast)
		}
		// The reader calls finalize only after DONE or authenticated clean EOF.
		events := FinalizeChatCompletionsResponsesStream(st)
		ae := FinalizeChatCompletionsAnthropicStream(ast)
		if st.Err() != nil || ast.Err() != nil || len(events) == 0 || len(ae) < 2 {
			t.Fatalf("finish errors: %v / %v", st.Err(), ast.Err())
		}
		if events[len(events)-1].Type != "response.completed" || ae[len(ae)-2].Delta.StopReason != "tool_use" || ae[len(ae)-1].Type != "message_stop" {
			t.Fatal("authenticated finish did not complete")
		}
		output := events[len(events)-1].Response.Output
		if len(output) != 1 || output[0].Arguments != `{"a":1}` {
			t.Fatalf("arguments=%#v", output)
		}
		if tailUsage && events[len(events)-1].Response.Usage.TotalTokens != 7 {
			t.Fatal("tail usage lost")
		}
	}
}

func TestM4ReviewAnthropicTruncatedTools(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		for _, args := range []string{`{"q":`, `{}`, ``} {
			st := NewChatCompletionsToAnthropicStreamState("m")
			st.Bridge = &BridgeOptions{Enabled: true}
			index := 0
			second := 1
			before, after := "before", "after"
			var events []AnthropicStreamEvent
			for _, delta := range []ChatDelta{{Content: &before}, {ToolCalls: []ChatToolCall{{Index: &index, ID: "call_1", Function: ChatFunctionCall{Name: "f", Arguments: args}}}}, {Content: &after}, {ToolCalls: []ChatToolCall{{Index: &second, ID: "call_2", Function: ChatFunctionCall{Name: "g", Arguments: args}}}}} {
				events = append(events, ChatCompletionsChunkToAnthropicEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: delta}}}, st)...)
			}
			ChatCompletionsChunkToAnthropicEvents(&ChatCompletionsChunk{Choices: []ChatChunkChoice{{FinishReason: &reason}}, Usage: &ChatUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}, st)
			events = append(events, FinalizeChatCompletionsAnthropicStream(st)...)
			if st.Err() != nil {
				t.Fatalf("%s %q: %v", reason, args, st.Err())
			}
			want := "max_tokens"
			if reason == "content_filter" {
				want = "refusal"
			}
			if len(events) < 2 || events[len(events)-1].Type != "message_stop" || events[len(events)-2].Delta.StopReason != want || events[len(events)-2].Usage.OutputTokens != 2 {
				t.Fatalf("terminal: %#v", events)
			}
			text := ""
			toolArgs := map[int]string{}
			open := map[int]bool{}
			for _, e := range events {
				switch e.Type {
				case "content_block_start":
					open[*e.Index] = true
					if e.ContentBlock.Type == "tool_use" {
						toolArgs[*e.Index] = ""
					}
				case "content_block_delta":
					if !open[*e.Index] {
						t.Fatal("delta after block close")
					}
					if e.Delta.Type == "text_delta" {
						text += e.Delta.Text
					}
					if e.Delta.Type == "input_json_delta" {
						toolArgs[*e.Index] += e.Delta.PartialJSON
					}
				case "content_block_stop":
					delete(open, *e.Index)
				}
			}
			if text != "beforeafter" || len(toolArgs) != 2 || len(open) != 0 {
				t.Fatalf("queue: text=%q args=%#v open=%v", text, toolArgs, open)
			}
			for _, got := range toolArgs {
				if got != args {
					t.Fatalf("partial arguments changed: %q != %q", got, args)
				}
			}
			buffered, err := ChatCompletionsResponseToAnthropicWithBridgeOptions(&ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Content: json.RawMessage(`"beforeafter"`), ToolCalls: []ChatToolCall{{ID: "call_1", Function: ChatFunctionCall{Name: "f", Arguments: args}}}}, FinishReason: reason}}}, "m", ConversionOptions{Bridge: st.Bridge})
			if err != nil || AnthropicStopReasonString(buffered.StopReason) != want {
				t.Fatalf("buffered differs: %#v %v", buffered, err)
			}
		}
	}
}
