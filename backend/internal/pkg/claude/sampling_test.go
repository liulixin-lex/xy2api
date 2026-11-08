package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRejectsSamplingParameters(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"claude-opus-4-6", false},
		{"claude-opus-4-7", true},
		{"claude-opus-4-8", true},
		{"claude-opus-5", true},
		{"claude-opus-5-5", true},
		{"claude-opus-4-20250514", false},
		{"claude-opus-4-1-20250805", false},
		{"claude-opus-4-6-20260205", false},
		{"claude-opus-4-7-20260417", true},
		{"claude-opus-5-20260601", true},
		{"claude-opus-5-5-20260922", true},
		{"anthropic.claude-opus-4-7", true},
		{"us.anthropic.claude-opus-4-8", true},
		{"eu.anthropic.claude-opus-4-7-v1:0", true},
		{"global.anthropic.claude-opus-5-v1:0", true},
		{"bedrock/us.anthropic.claude-opus-4-7-20260417-v1:0", true},
		{"anthropic.claude-opus-4-20250514-v1:0", false},
		{"claude-opus-4-7@20260417", true},
		{"claude-opus-4@20250514", false},
		{"anthropic/claude-opus-4.7", true},
		{"anthropic/claude-opus-5.5", true},
		{"models/claude-opus-4-7-thinking", true},
		{" Claude-Opus-4-8 ", true},
		{"claude-sonnet-5-5", false},
		{"claude-haiku-5-5", false},
		{"claude-fable-5", false},
		{"claude-fable-5-1", false},
		{"claude-3-opus-20240229", false},
		{"not-claude-opus-4-7", false},
		{"claude-opus-4-7-extra", false},
		{"claude-opus-4-7x", false},
		{"claude-opus-5x", false},
		{"", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			require.Equal(t, tc.want, RejectsSamplingParameters(tc.model))
		})
	}
}
