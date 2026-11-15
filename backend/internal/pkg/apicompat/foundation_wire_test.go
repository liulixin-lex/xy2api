//go:build unit

package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFoundationWireCarriers(t *testing.T) {
	for _, raw := range []string{`{"type":"reasoning"}`, `{"type":"reasoning","encrypted_content":null}`, `{"type":"reasoning","encrypted_content":""}`, `{"type":"reasoning","encrypted_content":"opaque","encrypted_function_args":null}`} {
		var in ResponsesInputItem
		var out ResponsesOutput
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		for _, v := range []any{in, out} {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			_ = json.Unmarshal(b, &got)
			_ = json.Unmarshal([]byte(raw), &want)
			if len(got) != len(want) || got["encrypted_content"] != want["encrypted_content"] {
				t.Fatalf("%s -> %s", raw, b)
			}
		}
	}
	var e ResponsesStreamEvent
	if err := json.Unmarshal([]byte(`{"output_index":0,"input":"","arguments":"","refusal":"no"}`), &e); err != nil {
		t.Fatal(err)
	}
	if !e.OutputIndexPresent || !e.InputPresent || !e.ArgumentsPresent || e.Refusal != "no" {
		t.Fatalf("%+v", e)
	}
	for _, raw := range []string{`{}`, `{"output_index":null,"input":null,"arguments":null}`} {
		if err := json.Unmarshal([]byte(raw), &e); err != nil || e.OutputIndexPresent || e.InputPresent || e.ArgumentsPresent {
			t.Fatalf("%s: %+v %v", raw, e, err)
		}
	}
	if json.Unmarshal([]byte(`{"input":1}`), &e) == nil {
		t.Fatal("accepted non-string input")
	}
	var delta ChatDelta
	if json.Unmarshal([]byte(`{"content":[]}`), &delta) == nil {
		t.Fatal("changed default ChatDelta decoder")
	}
	var tool AnthropicTool
	if err := json.Unmarshal([]byte(`{"name":"x","strict":false,"allowed_callers":["direct"]}`), &tool); err != nil || tool.Strict == nil || *tool.Strict {
		t.Fatal(tool, err)
	}
	var rt ResponsesTool
	if err := json.Unmarshal([]byte(`{"type":"custom","format":{"type":"text"}}`), &rt); err != nil || len(rt.Format) == 0 {
		t.Fatal(rt, err)
	}
	var action WebSearchAction
	if err := json.Unmarshal([]byte(`{"url":"https://example.test","sources":[{"type":"url","url":"https://example.test","title":"t","content":"c"}]}`), &action); err != nil || len(action.Sources) != 1 {
		t.Fatal(action, err)
	}
	req := &ResponsesRequest{Model: "m"}
	before, _ := json.Marshal(req)
	if err := MarkConversionTarget(req, "gemini"); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) || strings.Contains(string(after), "conversionTarget") {
		t.Fatal(string(after))
	}
	if MarkConversionTarget(nil, "gemini") == nil || MarkConversionTarget(req, "chat") == nil {
		t.Fatal("accepted invalid target")
	}
	if (BridgeOptions{}).Enabled {
		t.Fatal("bridge enabled by default")
	}
}
