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
