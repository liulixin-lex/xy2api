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
		{"responses", "failed", ""}, {"responses", "cancelled", ""}, {"responses", "canceled", ""}, {"responses", "", ""},
		{"gemini", "", "OTHER"},
		{"chat", "", "tool_calls"}, {"chat", "", "function_call"}, {"anthropic", "", "tool_use"}, {"bad", "", "stop"},
	} {
		cases = append(cases, terminalCase{source[0], source[1], source[2], false, false, [4]string{"ERR", "ERR", "ERR", "ERR"}})
	}
	for _, tc := range cases {
		for i, target := range targets {
			t.Run(tc.source+"/"+tc.status+"/"+tc.reason+"/"+target, func(t *testing.T) {
				status, reason, mapped, err := ProjectTerminalWithMapping(tc.source, target, tc.status, tc.reason, tc.tools, tc.refusal)
				if mapped {
					t.Fatal("known terminal must not be marked unknown")
				}
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

func TestFoundationTerminalUnknownMapping(t *testing.T) {
	// These are the pre-foundation direction mappers, retained by their owners.
	for _, reason := range []string{"eos_token", "model_length"} {
		for _, tools := range []bool{false, true} {
			var blocks []AnthropicContentBlock
			if tools {
				blocks = []AnthropicContentBlock{{Type: "tool_use"}}
			}
			want := chatFinishReasonToAnthropicStopReason(reason, blocks)
			if other := ccFinishReasonToAnthropicStopReason(reason, tools); other != want {
				t.Fatalf("baseline mappers disagree: %s/%s", want, other)
			}
			status, got, mapped, err := ProjectTerminalWithMapping("chat", "anthropic", "", reason, tools, false)
			if err != nil || status != "" || got != want || !mapped {
				t.Fatalf("%s tools=%t: %s/%s mapped=%t err=%v; want %s", reason, tools, status, got, mapped, err, want)
			}
		}
	}
	status, reason, mapped, err := ProjectTerminalWithMapping("anthropic", "responses", "", "some_future_reason", false, false)
	if want := anthropicStopReasonToResponsesStatus("some_future_reason", nil); err != nil || status != want || reason != "" || !mapped {
		t.Fatalf("Anthropic future: %s/%s mapped=%t err=%v; want %s", status, reason, mapped, err, want)
	}
	// Each target keeps its own normal finish vocabulary; tools remain complete.
	for _, source := range [][3]string{
		{"chat", "", "eos_token"}, {"chat", "", "model_length"},
		{"anthropic", "", "some_future_reason"}, {"gemini", "", "FUTURE_REASON"},
		{"responses", "future_status", ""}, {"responses", "completed", "future_reason"}, {"responses", "incomplete", "future_reason"},
	} {
		for _, tools := range []bool{false, true} {
			want := map[string][2]string{"responses": {"completed", ""}, "chat": {"", "stop"}, "anthropic": {"", "end_turn"}, "gemini": {"", "STOP"}}
			if tools {
				want["chat"] = [2]string{"", "tool_calls"}
				want["anthropic"] = [2]string{"", "tool_use"}
			}
			for target, expected := range want {
				status, reason, mapped, err := ProjectTerminalWithMapping(source[0], target, source[1], source[2], tools, false)
				if err != nil || !mapped || [2]string{status, reason} != expected {
					t.Fatalf("%v -> %s tools=%t: %s/%s mapped=%t err=%v; want %v", source, target, tools, status, reason, mapped, err, expected)
				}
				oldStatus, oldReason, oldErr := ProjectTerminal(source[0], target, source[1], source[2], tools, false)
				if oldErr != nil || oldStatus != status || oldReason != reason {
					t.Fatalf("compatibility wrapper differs: %s/%s %v", oldStatus, oldReason, oldErr)
				}
			}
		}
	}
}
