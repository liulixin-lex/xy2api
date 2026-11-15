//go:build unit

package apicompat

import (
	"errors"
	"testing"
)

func TestFoundationTerminalProjection(t *testing.T) {
	targets := []string{"responses", "chat", "anthropic", "gemini"}
	type terminalCase struct {
		source, status, reason string
		tools, refusal         bool
		want                   [4]string
	}
	cases := []terminalCase{
		{"responses", "completed", "", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"responses", "completed", "", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"chat", "", "stop", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"chat", "", "stop", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"anthropic", "", "end_turn", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"anthropic", "", "end_turn", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"gemini", "", "STOP", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"gemini", "", "STOP", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"chat", "", "tool_calls", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"chat", "", "function_call", true, false, [4]string{"completed/", "/function_call", "/tool_use", "/STOP"}},
		{"anthropic", "", "tool_use", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"anthropic", "", "stop_sequence", false, false, [4]string{"completed/", "/stop", "/stop_sequence", "/STOP"}},
		{"responses", "incomplete", "max_output_tokens", true, true, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"responses", "incomplete", "", false, false, [4]string{"incomplete/", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"chat", "", "length", true, true, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"anthropic", "", "max_tokens", true, true, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"gemini", "", "MAX_TOKENS", true, true, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"anthropic", "", "model_context_window_exceeded", true, false, [4]string{"incomplete/max_output_tokens", "/length", "/model_context_window_exceeded", "/MAX_TOKENS"}},
		{"responses", "incomplete", "content_filter", true, true, [4]string{"incomplete/content_filter", "/content_filter", "/refusal", "/SAFETY"}},
		{"chat", "", "content_filter", true, true, [4]string{"incomplete/content_filter", "/content_filter", "/refusal", "/SAFETY"}},
		{"responses", "completed", "", true, true, [4]string{"completed/", "/stop", "/refusal", "/SAFETY"}},
		{"chat", "", "stop", true, true, [4]string{"completed/", "/stop", "/refusal", "/SAFETY"}},
		{"anthropic", "", "refusal", true, false, [4]string{"completed/", "/stop", "/refusal", "/SAFETY"}},
		{"anthropic", "", "pause_turn", false, false, [4]string{"ERR", "ERR", "/pause_turn", "ERR"}},
		{"chat", "", "", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"anthropic", "", "", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"gemini", "", "", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
	}
	for _, reason := range []string{"SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION"} {
		cases = append(cases, terminalCase{"gemini", "", reason, true, false, [4]string{"incomplete/content_filter", "/content_filter", "/refusal", "/" + reason}})
	}
	for _, reason := range []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS"} {
		cases = append(cases, terminalCase{"gemini", "", reason, false, false, [4]string{"ERR", "ERR", "ERR", "/" + reason}})
	}
	for _, source := range [][3]string{
		{"responses", "failed", ""}, {"responses", "cancelled", ""}, {"responses", "canceled", ""}, {"responses", "unknown", ""}, {"responses", "completed", "unknown"}, {"responses", "incomplete", "unknown"}, {"responses", "", ""},
		{"chat", "", "unknown"}, {"anthropic", "", "unknown"}, {"gemini", "", "OTHER"}, {"gemini", "", "unknown"},
		{"chat", "", "tool_calls"}, {"chat", "", "function_call"}, {"anthropic", "", "tool_use"}, {"bad", "", "stop"},
	} {
		cases = append(cases, terminalCase{source[0], source[1], source[2], false, false, [4]string{"ERR", "ERR", "ERR", "ERR"}})
	}
	for _, tc := range cases {
		for i, target := range targets {
			t.Run(tc.source+"/"+tc.status+"/"+tc.reason+"/"+target, func(t *testing.T) {
				status, reason, err := ProjectTerminal(tc.source, target, tc.status, tc.reason, tc.tools, tc.refusal)
				if tc.want[i] == "ERR" {
					var conversion *UpstreamConversionError
					if !errors.As(err, &conversion) || status != "" || reason != "" {
						t.Fatalf("%+v -> %s: %q/%q %v", tc, target, status, reason, err)
					}
					return
				}
				if err != nil || status+"/"+reason != tc.want[i] {
					t.Fatalf("%+v -> %s: %q/%q %v; want %s", tc, target, status, reason, err, tc.want[i])
				}
			})
		}
	}
	if status, reason, err := ProjectTerminal("chat", "bad", "", "stop", false, false); err == nil || status != "" || reason != "" {
		t.Fatal(status, reason, err)
	}
	for _, status := range []string{"completed", "incomplete", "failed"} {
		got, err := ResponsesTerminalEventType(status)
		if err != nil || got != "response."+status {
			t.Fatal(got, err)
		}
	}
	for _, status := range []string{"", "done", "cancelled", "unknown"} {
		got, err := ResponsesTerminalEventType(status)
		if err == nil || got != "" {
			t.Fatal(status, got, err)
		}
	}
}

func TestFoundationToolArguments(t *testing.T) {
	for _, tc := range []struct {
		kind, raw          string
		object, opaque, ok bool
	}{
		{"function", `{}`, true, false, true}, {"function", `null`, true, false, false}, {"function", `[]`, true, false, false}, {"function", `[]`, false, false, true}, {"function", `{"a":`, false, false, false}, {"function", "", false, false, false}, {"custom", "", true, false, true}, {"function", "opaque", true, true, true}, {"unknown", `{}`, false, true, false},
	} {
		if err := ValidateToolArguments(tc.kind, tc.raw, tc.object, tc.opaque); (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
}
