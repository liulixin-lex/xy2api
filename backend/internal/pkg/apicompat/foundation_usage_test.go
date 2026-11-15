//go:build unit

package apicompat

import (
	"encoding/json"
	"testing"
)

func TestFoundationUsagePresence(t *testing.T) {
	// These exact strings are the pre-change Marshal output, including field order.
	for _, tc := range []struct {
		v    any
		want string
	}{
		{ResponsesUsage{}, `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`},
		{ResponsesUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}, `{"input_tokens":5,"output_tokens":2,"total_tokens":7}`},
		{ChatUsage{}, `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`},
		{ChatUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, `{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}`},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil || string(b) != tc.want {
			t.Fatalf("baseline mismatch: %s, %v", b, err)
		}
	}
	for _, tc := range []struct {
		raw            string
		in, out, total bool
	}{
		{`{}`, false, false, false}, {`null`, false, false, false},
		{`{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, true, true, true},
		{`{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}`, true, true, true},
		{`{"input_tokens":5}`, true, false, false},
		{`{"input_tokens":null,"output_tokens":"2","total_tokens":-1}`, false, false, false},
		{`{"input_tokens":1.5,"output_tokens":true,"total_tokens":{}}`, false, false, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var r ResponsesUsage
			var c ChatUsage
			if err := json.Unmarshal([]byte(tc.raw), &r); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.raw), &c); err != nil {
				t.Fatal(err)
			}
			if !r.UsagePresenceKnown || r.InputTokensPresent != tc.in || r.OutputTokensPresent != tc.out || r.TotalTokensPresent != tc.total || !c.UsagePresenceKnown || c.PromptTokensPresent != tc.in || c.CompletionTokensPresent != tc.out || c.TotalTokensPresent != tc.total {
				t.Fatalf("r=%+v c=%+v", r, c)
			}
			for _, v := range []any{r, c} {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(b, &fields)
				_, total := fields["total_tokens"]
				if total != tc.total {
					t.Fatalf("invented total: %s", b)
				}
			}
		})
	}
}
