//go:build unit

package apicompat

import (
	"encoding/json"
	"reflect"
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
			// Re-encoding a lowered schema may sort its keys; the content must not change.
			var beforeValue, afterValue any
			_ = json.Unmarshal(before, &beforeValue)
			_ = json.Unmarshal(after, &afterValue)
			if !reflect.DeepEqual(beforeValue, afterValue) {
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

func TestM4ReviewAnthropicEOFTerminal(t *testing.T) {
	for _, kind := range []string{"text", "thinking"} {
		st := NewAnthropicEventToResponsesState()
		opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}}
		var events []ResponsesStreamEvent
		for _, raw := range []string{`{"type":"message_start","message":{"id":"fixed","model":"m","usage":{"input_tokens":5}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"` + kind + `"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"` + kind + `_delta","` + kind + `":"kept"}}`, `{"type":"content_block_stop","index":0}`} {
			var e AnthropicStreamEvent
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				t.Fatal(err)
			}
			events = append(events, AnthropicEventToResponsesEventsWithBridgeOptions(&e, st, opts)...)
		}
		events = append(events, FinalizeAnthropicResponsesStreamWithBridgeOptions(st, opts)...)
		count := 0
		for _, e := range events {
			if e.Type == "response.completed" {
				count++
				raw, _ := json.Marshal(e.Response)
				if !strings.Contains(string(raw), "kept") || e.Response.Usage.InputTokens != 5 {
					t.Fatalf("lost content/usage: %s", raw)
				}
			}
		}
		if count != 1 || st.Err() != nil || !st.CompletedSent || len(FinalizeAnthropicResponsesStreamWithBridgeOptions(st, opts)) != 0 {
			t.Fatalf("EOF terminal: count=%d err=%v", count, st.Err())
		}
	}
}

func TestM4ReviewNestedThinkingAndUnknownParts(t *testing.T) {
	content := json.RawMessage(`[{"type":"thinking","thinking":[{"type":"text","text":"T"},"2",{"text":"3"}]},{"type":"reference","id":"r"},{"type":"text","text":"ANSWER"}]`)
	var notices []ConversionNotice
	record := func(n ConversionNotice) { notices = append(notices, n) }
	opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}, Record: record}
	response := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: content}, FinishReason: "stop"}}}
	for _, anthropic := range []bool{false, true} {
		notices = nil
		var out any
		var err error
		if anthropic {
			out, err = ChatCompletionsResponseToAnthropicWithBridgeOptions(response, "m", opts)
		} else {
			out, err = ChatCompletionsResponseToResponsesWithBridgeOptions(response, "m", nil, nil, false, nil, opts)
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), "T23") || !strings.Contains(string(raw), "ANSWER") || strings.Contains(string(raw), "reference") || len(notices) != 1 || notices[0] != (ConversionNotice{Field: "choices[0].message.content[1]", Action: "omit"}) {
			t.Fatalf("buffered: %s notices=%#v", raw, notices)
		}
		notices = nil
		first, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}}})
		answer := []byte(`{"choices":[{"index":0,"delta":{"content":"NEXT"},"finish_reason":"stop"}]}`)
		if anthropic {
			state := NewChatCompletionsToAnthropicStreamState("m")
			state.Bridge = opts.Bridge
			state.SetConversionRecorder(record)
			events := ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(first, state)
			events = append(events, ChatCompletionsChunkToAnthropicEventsWithBridgeOptions(answer, state)...)
			events = append(events, FinalizeChatCompletionsAnthropicStream(state)...)
			if state.Err() != nil {
				t.Fatal(state.Err())
			}
			raw, _ = json.Marshal(events)
		} else {
			state := NewChatCompletionsToResponsesStreamState("m")
			state.Bridge = opts.Bridge
			state.SetConversionRecorder(record)
			events := ChatCompletionsChunkToResponsesEventsWithBridgeOptions(first, state)
			events = append(events, ChatCompletionsChunkToResponsesEventsWithBridgeOptions(answer, state)...)
			events = append(events, FinalizeChatCompletionsResponsesStream(state)...)
			if state.Err() != nil {
				t.Fatal(state.Err())
			}
			raw, _ = json.Marshal(events)
		}
		if !strings.Contains(string(raw), "T23") || !strings.Contains(string(raw), "ANSWER") || !strings.Contains(string(raw), "NEXT") || len(notices) != 1 || notices[0] != (ConversionNotice{Field: "choices[0].delta.content[1]", Action: "omit"}) {
			t.Fatalf("stream: %s notices=%#v", raw, notices)
		}
	}
	for _, raw := range []string{`[{"text":"missing type"}]`, `[{"type":"text","text":3}]`, `[{"type":"thinking","thinking":[{"text":false}]}]`, `[{`} {
		if _, err := DecodeChatOutputContent(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid output accepted: %s", raw)
		}
	}
}

func TestM4ReviewChatUserMetadataOmission(t *testing.T) {
	for _, user := range []string{`123`, `{"id":"u"}`, `true`, `"alice"`, `null`} {
		var req ResponsesRequest
		if err := json.Unmarshal([]byte(`{"model":"m","input":"hi","user":`+user+`,"metadata":{"trace":"abc"}}`), &req); err != nil {
			t.Fatal(err)
		}
		var notices []ConversionNotice
		out, err := ResponsesToChatCompletionsRequestWithOptions(&req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}, Record: func(n ConversionNotice) { notices = append(notices, n) }})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Metadata) != 0 || (user == `"alice"` && string(out.User) != user) || (user != `"alice"` && len(out.User) != 0) {
			t.Fatalf("fields: %#v", out)
		}
		want := 1
		if user != `"alice"` && user != `null` {
			want = 2
		}
		if len(notices) != want || notices[len(notices)-1] != (ConversionNotice{Field: "metadata", Action: "omit"}) {
			t.Fatalf("notices=%#v", notices)
		}
		legacy, err := ResponsesToChatCompletionsRequest(&req)
		if err != nil {
			t.Fatal(err)
		}
		if user != `"alice"` {
			a, _ := json.Marshal(out)
			b, _ := json.Marshal(legacy)
			if string(a) != string(b) {
				t.Fatalf("baseline fields differ: %s / %s", a, b)
			}
		}
	}
}

func TestM4ReviewEmptyChatAllowedTools(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hi","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"gone"}]}}`), &req); err != nil {
		t.Fatal(err)
	}
	var notices []ConversionNotice
	out, err := ResponsesToChatCompletionsRequestWithOptions(&req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}, Record: func(n ConversionNotice) { notices = append(notices, n) }})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if len(out.Tools) != 0 || len(out.ToolChoice) != 0 || strings.Contains(string(raw), `"tools"`) || strings.Contains(string(raw), `"tool_choice"`) {
		t.Fatalf("orphan choice sent: %s", raw)
	}
	if len(notices) != 1 || notices[0] != (ConversionNotice{Field: "tool_choice.tools[0]", Action: "omit"}) {
		t.Fatalf("notice=%#v", notices)
	}
}

func TestM4ReviewMediaDetailValidation(t *testing.T) {
	for _, tc := range []struct {
		kind, source, detail string
		bad                  bool
	}{
		{"input_image", `"image_url":"https://example.test/a.png"`, "high", false},
		{"input_image", `"image_url":"https://example.test/a.png"`, "low", false},
		{"input_image", `"image_url":"https://example.test/a.png"`, "auto", false},
		{"input_image", `"image_url":"https://example.test/a.png"`, "bogus", true},
		{"input_file", `"file_url":"https://example.test/a.pdf"`, "high", true},
		{"input_file", `"file_url":"https://example.test/a.pdf"`, "auto", false},
	} {
		req := &ResponsesRequest{Model: "m", Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"` + tc.kind + `",` + tc.source + `,"detail":"` + tc.detail + `"}]}]`)}
		var notices []ConversionNotice
		out, err := ResponsesToAnthropicRequestWithOptions(req, ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }})
		if (err != nil) != tc.bad {
			t.Fatalf("%s/%s err=%v", tc.kind, tc.detail, err)
		}
		// The legacy entry shares M2's detail rule; only the error path differs.
		_, legacyErr := ResponsesToAnthropicRequest(req)
		if tc.bad {
			if !strings.Contains(err.Error(), "input[0].content[1].detail") || len(notices) != 0 {
				t.Fatalf("invalid detail: %v %#v", err, notices)
			}
			if legacyErr == nil || !strings.Contains(legacyErr.Error(), "content.detail") {
				t.Fatalf("legacy invalid detail: %v", legacyErr)
			}
			continue
		}
		if legacyErr != nil {
			t.Fatalf("legacy valid detail: %v", legacyErr)
		}
		raw, _ := json.Marshal(out.Messages)
		if !strings.Contains(string(raw), "https://example.test/") {
			t.Fatalf("media lost: %s", raw)
		}
		want := 0
		if tc.kind == "input_image" {
			want = 1
		}
		if len(notices) != want {
			t.Fatalf("notice=%#v", notices)
		}
		if want == 1 && notices[0] != (ConversionNotice{Field: "input[0].content[1].detail", Action: "omit"}) {
			t.Fatalf("notice=%#v", notices)
		}
	}
}
