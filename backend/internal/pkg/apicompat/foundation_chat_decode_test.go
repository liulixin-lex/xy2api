//go:build unit

package apicompat

import (
	"encoding/json"
	"testing"
)

func TestFoundationLegacyChatDecode(t *testing.T) {
	const baseline = `{"model":"probe","input":[{"type":"message","role":"user","content":"hi"}],"stream":true,"include":["reasoning.encrypted_content"],"store":false}`
	for _, extra := range []string{`"verbosity":1`, `"text":"ignored"`, `"text":{"verbosity":1}`, `"max_output_tokens":"ignored"`, `"max_output_tokens":1.5`, `"metadata":123,"user":123,"reasoning":[1],"include":false`} {
		t.Run(extra, func(t *testing.T) {
			var req ChatCompletionsRequest
			raw := `{"model":"probe","messages":[{"role":"user","content":"hi"}],` + extra + `}`
			if err := json.Unmarshal([]byte(raw), &req); err != nil {
				t.Fatal(err)
			}
			converted, err := ChatCompletionsToResponses(&req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(converted)
			if err != nil || string(got) != baseline {
				t.Fatalf("%s: %s %v", raw, got, err)
			}
		})
	}
	var valid ChatCompletionsRequest
	if err := json.Unmarshal([]byte(`{"verbosity":"low","text":{"verbosity":"high","format":{"type":"text"}},"max_output_tokens":17}`), &valid); err != nil {
		t.Fatal(err)
	}
	if valid.Verbosity != "low" || valid.Text == nil || valid.Text.Verbosity != "high" || valid.MaxOutputTokens == nil || *valid.MaxOutputTokens != 17 {
		t.Fatalf("%+v", valid)
	}
	// Existing typed fields keep their original decoder validation.
	if json.Unmarshal([]byte(`{"max_tokens":"invalid"}`), &valid) == nil {
		t.Fatal("existing field validation removed")
	}
}
