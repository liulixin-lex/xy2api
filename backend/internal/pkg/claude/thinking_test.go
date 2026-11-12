package claude

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestThinkingModeForModel(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-4-20250514", "claude-3-7-sonnet-20250219", "relay-alias"} {
		require.Equal(t, "enabled", ThinkingModeForModel(model), model)
	}
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-7", "anthropic/claude-opus-4.8", "claude-sonnet-5", "claude-opus-5-5", "claude-fable-5-2", "claude-mythos-preview", "us.anthropic.claude-opus-4-6-v1:0", "claude-sonnet-4-6@20260101"} {
		require.Equal(t, "adaptive", ThinkingModeForModel(model), model)
	}
	require.False(t, RejectsNonDefaultSampling("claude-sonnet-4-6"))
	require.True(t, RejectsNonDefaultSampling("claude-opus-4.7"))
	require.True(t, RejectsNonDefaultSampling("claude-fable-5"))
}
