//go:build unit

package apicompat

import "testing"

func TestFoundationTerminalProjection(t *testing.T) {
	targets := []string{"responses", "chat", "anthropic", "gemini"}
	for _, tc := range []struct {
		source, status, reason string
		tools, refusal         bool
		want                   [4]string
	}{
		{"chat", "", "stop", false, false, [4]string{"completed/", "/stop", "/end_turn", "/STOP"}},
		{"anthropic", "", "end_turn", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"chat", "", "tool_calls", true, false, [4]string{"completed/", "/tool_calls", "/tool_use", "/STOP"}},
		{"anthropic", "", "stop_sequence", false, false, [4]string{"completed/", "/stop", "/stop_sequence", "/STOP"}},
		{"responses", "incomplete", "max_output_tokens", true, false, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"responses", "incomplete", "", false, false, [4]string{"incomplete/", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"chat", "", "length", true, false, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"anthropic", "", "max_tokens", true, false, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"gemini", "", "MAX_TOKENS", true, false, [4]string{"incomplete/max_output_tokens", "/length", "/max_tokens", "/MAX_TOKENS"}},
		{"anthropic", "", "model_context_window_exceeded", true, false, [4]string{"incomplete/max_output_tokens", "/length", "/model_context_window_exceeded", "/MAX_TOKENS"}},
		{"responses", "incomplete", "content_filter", true, false, [4]string{"incomplete/content_filter", "/content_filter", "/refusal", "/SAFETY"}},
		{"chat", "", "content_filter", true, false, [4]string{"incomplete/content_filter", "/content_filter", "/refusal", "/SAFETY"}},
		{"responses", "completed", "", true, true, [4]string{"completed/", "/stop", "/refusal", "/SAFETY"}},
		{"anthropic", "", "refusal", true, false, [4]string{"completed/", "/stop", "/refusal", "/SAFETY"}},
	} {
		for i, target := range targets {
			s, r, err := ProjectTerminal(tc.source, target, tc.status, tc.reason, tc.tools, tc.refusal)
			if err != nil || s+"/"+r != tc.want[i] {
				t.Fatalf("%+v -> %s: %s/%s %v", tc, target, s, r, err)
			}
		}
	}
	for _, reason := range []string{"SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION"} {
		for _, target := range targets {
			_, r, err := ProjectTerminal("gemini", target, "", reason, true, false)
			if err != nil || target == "gemini" && r != reason {
				t.Fatal(reason, target, r, err)
			}
		}
	}
	for _, tc := range [][4]string{{"responses", "chat", "failed", ""}, {"responses", "chat", "cancelled", ""}, {"gemini", "chat", "", "OTHER"}, {"anthropic", "chat", "", "pause_turn"}, {"chat", "responses", "", "tool_calls"}, {"bad", "chat", "", "stop"}, {"chat", "bad", "", "stop"}} {
		s, r, err := ProjectTerminal(tc[0], tc[1], tc[2], tc[3], false, false)
		if err == nil || s != "" || r != "" {
			t.Fatal(tc, s, r, err)
		}
	}
	for _, s := range []string{"completed", "incomplete", "failed"} {
		if got, err := ResponsesTerminalEventType(s); err != nil || got != "response."+s {
			t.Fatal(got, err)
		}
	}
	if _, err := ResponsesTerminalEventType("done"); err == nil {
		t.Fatal("accepted done")
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
