//go:build unit

package claude

import "testing"

func TestClaude52(t *testing.T) {
	for _, id := range []string{"claude-fable-5-2", "claude-mythos-5-2", "anthropic/claude-fable-5-2", "us.anthropic.claude-mythos-5-2-thinking"} {
		spec, ok := Claude52(id)
		if !ok || spec.DefaultEffort != "high" || !spec.PreserveSignedThinking || spec.ForcedToolChoice {
			t.Fatal(id, spec, ok)
		}
	}
	for _, id := range []string{"claude-fable-5.2", "claude-mythos-5-2-future", "claude-fable-5-2-20260101", "claude-opus-5-5", "unknown"} {
		if _, ok := Claude52(id); ok {
			t.Fatal(id)
		}
	}
	if _, ok := Claude55("claude-sonnet-5-5"); !ok {
		t.Fatal("5.5 changed")
	}
}
