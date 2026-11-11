package apicompat

import (
	"encoding/json"
	"reflect"
	"testing"
)

// These regressions also run against the pristine and restored package.
func TestProtocolLossRegression(t *testing.T) {
	t.Run("controls", func(t *testing.T) {
		var chat ChatCompletionsRequest
		raw := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"stop":["END","\nDONE"],"max_tokens":500,"max_completion_tokens":1,"parallel_tool_calls":false,"tools":[{"type":"function","function":{"name":"probe","parameters":{"type":"object"}}}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}}}}`
		if err := json.Unmarshal([]byte(raw), &chat); err != nil {
			t.Fatal(err)
		}
		responses, err := ChatCompletionsToResponses(&chat)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ResponsesToAnthropicRequest(responses)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Anthropic wire: %s", wire)
		if got.MaxTokens != 1 {
			t.Errorf("max_tokens = %d, want 1", got.MaxTokens)
		}
		if !reflect.DeepEqual(got.StopSeqs, []string{"END", "\nDONE"}) {
			t.Errorf("stop_sequences = %q", got.StopSeqs)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		var output map[string]json.RawMessage
		_ = json.Unmarshal(fields["output_config"], &output)
		if len(output["format"]) == 0 {
			t.Error("output_config.format missing")
		}
		var choice map[string]any
		_ = json.Unmarshal(got.ToolChoice, &choice)
		if choice["disable_parallel_tool_use"] != true {
			t.Errorf("tool_choice = %s", got.ToolChoice)
		}
	})
	t.Run("media", func(t *testing.T) {
		var req ResponsesRequest
		raw := `{"model":"claude-sonnet-4-5","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/image.png?version=1"},{"type":"input_file","file_url":"https://example.test/report.pdf","filename":"report.pdf"}]}]}`
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		got, err := ResponsesToAnthropicRequest(&req)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(got)
		t.Logf("Media wire: %s", wire)
		if len(got.Messages) != 1 {
			t.Fatalf("messages = %d, want 1", len(got.Messages))
		}
		var blocks []map[string]any
		if err := json.Unmarshal(got.Messages[0].Content, &blocks); err != nil {
			t.Fatal(err)
		}
		if len(blocks) != 2 {
			t.Fatalf("media blocks = %d, want 2", len(blocks))
		}
		for i, want := range []string{"https://example.test/image.png?version=1", "https://example.test/report.pdf"} {
			source, _ := blocks[i]["source"].(map[string]any)
			if source["url"] != want || source["type"] != "url" {
				t.Errorf("source[%d] = %v", i, source)
			}
		}
	})
	t.Run("unsupported_file_id", func(t *testing.T) {
		req := ResponsesRequest{Model: "claude-sonnet-4-5", Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"read this"},{"type":"input_file","file_id":"file-provider-owned"}]}]`)}
		_, err := ResponsesToAnthropicRequest(&req)
		t.Logf("Unsupported file result: %v", err)
		if err == nil {
			t.Error("provider-specific file ID was silently dropped")
		}
	})
}
