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
