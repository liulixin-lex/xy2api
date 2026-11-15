//go:build unit

package apicompat

import "testing"

func TestEffectiveRequestMaxOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		protocol, raw, field string
		n                    int
		bad                  bool
	}{
		{"chat", `{"max_completion_tokens":1,"max_tokens":1000}`, "max_completion_tokens", 1, false},
		{"chat", `{"max_completion_tokens":null,"max_tokens":2,"max_output_tokens":3}`, "max_tokens", 2, false},
		{"chat", `{"max_output_tokens":3}`, "max_output_tokens", 3, false},
		{"responses", `{"max_tokens":1000}`, "", 0, false},
		{"anthropic", `{"max_tokens":1}`, "max_tokens", 1, false},
		{"gemini", `{"generationConfig":{"maxOutputTokens":1},"generation_config":{"max_output_tokens":1000}}`, "generationConfig.maxOutputTokens", 1, false},
		{"gemini", `{"generation_config":{"max_output_tokens":1000}}`, "generation_config.max_output_tokens", 1000, false},
		{"chat", `{}`, "", 0, false}, {"chat", `null`, "", 0, true}, {"chat", `[]`, "", 0, true}, {"bad", `{}`, "", 0, true},
	} {
		v, f, err := EffectiveRequestMaxOutputTokens(tc.protocol, []byte(tc.raw))
		if (err != nil) != tc.bad || !tc.bad && (f != tc.field || v == nil && tc.n != 0 || v != nil && *v != tc.n) {
			t.Fatal(tc, v, f, err)
		}
	}
	for _, bad := range []string{"0", "-1", "1.5", "9223372036854775808", `"1"`} {
		v, f, err := EffectiveRequestMaxOutputTokens("chat", []byte(`{"max_completion_tokens":`+bad+`,"max_tokens":1000}`))
		if err == nil || v != nil || f != "max_completion_tokens" {
			t.Fatal(bad, v, f, err)
		}
	}
}

func TestEffectiveChatMaxOutputTokens(t *testing.T) {
	one, thousand, zero := 1, 1000, 0
	for _, tc := range []struct {
		req   *ChatCompletionsRequest
		want  int
		field string
		bad   bool
	}{
		{nil, 0, "", false},
		{&ChatCompletionsRequest{}, 0, "", false},
		{&ChatCompletionsRequest{MaxCompletionTokens: &one, MaxTokens: &thousand}, 1, "max_completion_tokens", false},
		{&ChatCompletionsRequest{MaxTokens: &one, MaxOutputTokens: &thousand}, 1, "max_tokens", false},
		{&ChatCompletionsRequest{MaxOutputTokens: &one}, 1, "max_output_tokens", false},
		{&ChatCompletionsRequest{MaxCompletionTokens: &zero, MaxTokens: &thousand}, 0, "max_completion_tokens", true},
	} {
		n, field, err := EffectiveChatMaxOutputTokens(tc.req)
		if (err != nil) != tc.bad || field != tc.field || n == nil && tc.want != 0 || n != nil && *n != tc.want {
			t.Fatal(tc, n, field, err)
		}
	}
}
