//go:build unit

package service

import "testing"

func TestOpenAIResponsesUsageEvidence(t *testing.T) {
	for _, source := range []string{"upstream_json", "upstream_sse_terminal", "upstream_sse_progressive"} {
		for _, tc := range []struct {
			data                 string
			present              bool
			input, output, cache int
		}{
			{`{}`, false, 0, 0, 0}, {`{"usage":null}`, false, 0, 0, 0}, {`{"usage":{}}`, false, 0, 0, 0},
			{`{"usage":{"input_tokens":0,"output_tokens":0}}`, true, 0, 0, 0},
			{`{"response":{"usage":{"input_tokens":5}}}`, true, 5, 0, 0},
			{`{"usage":{"input_tokens":null,"output_tokens":"2"}}`, false, 0, 0, 0},
			{`{"usage":{"input_tokens":-1,"output_tokens":1.5}}`, false, 0, 0, 0},
			{`{"usage":{"input_tokens":5,"output_tokens":2,"input_tokens_details":{"cached_tokens":0},"cache_read_input_tokens":9}}`, true, 5, 2, 0},
		} {
			u, ok := openAIResponsesUsageEvidence([]byte(tc.data), source)
			if ok != tc.present || u.InputTokens != tc.input || u.OutputTokens != tc.output || u.CacheReadInputTokens != tc.cache {
				t.Fatal(source, tc, u, ok)
			}
		}
	}
	if _, ok := openAIResponsesUsageEvidence([]byte(`{"usage":{"input_tokens":1}}`), "missing"); ok {
		t.Fatal("invalid source")
	}
}
