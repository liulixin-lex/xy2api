package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaude55SpecMatchesFullIDs(t *testing.T) {
	for _, model := range []string{
		"claude-haiku-5-5",
		"anthropic.claude-haiku-5-5",
		"us.anthropic.claude-haiku-5-5",
		"anthropic/claude-haiku-5.5",
		"claude-haiku-5-5-thinking",
	} {
		spec, ok := Claude55(model)
		require.True(t, ok, model)
		require.Equal(t, "claude-haiku-5-5", spec.ID, model)
		require.True(t, IsHaiku55(model), model)
	}
	for _, model := range []string{"claude-haiku-4-5", "claude-haiku-4-5-20251001", "claude-haiku-5", "claude-sonnet-5", "gpt-5.5"} {
		require.False(t, IsClaude55(model), model)
	}
	opus, ok := Claude55("claude-opus-5-5")
	require.True(t, ok)
	require.Equal(t, "medium", opus.DefaultEffort)
	require.False(t, opus.ForcedToolChoice)
	sonnet, ok := Claude55("claude-sonnet-5-5")
	require.True(t, ok)
	require.Equal(t, "high", sonnet.DefaultEffort)
	require.True(t, sonnet.BetweenTools)
	haiku, _ := Claude55("claude-haiku-5-5")
	require.Equal(t, "medium", haiku.DefaultEffort)
	require.True(t, haiku.ForcedToolChoice)
	require.Equal(t, "high", haiku.DisabledThinkingMaxEffort)
	require.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, EffortLevelsForModel("claude-haiku-5-5"))
	require.Nil(t, EffortLevelsForModel("claude-haiku-4-5"))
}

func TestClaude55HaikuSampling(t *testing.T) {
	haiku, _ := Claude55("claude-haiku-5-5")
	num := func(v float64) SamplingValue { return SamplingValue{Set: true, Number: true, Value: v} }
	none := SamplingValue{}
	require.NoError(t, haiku.CheckSampling(none, none, false))
	require.NoError(t, haiku.CheckSampling(num(1), none, false))
	require.NoError(t, haiku.CheckSampling(none, num(0.99), false))
	require.ErrorContains(t, haiku.CheckSampling(num(0.7), none, false), "non-default temperature")
	require.ErrorContains(t, haiku.CheckSampling(SamplingValue{Set: true}, none, false), "non-default temperature")
	require.ErrorContains(t, haiku.CheckSampling(none, num(1), false), "non-default top_p")
	require.ErrorContains(t, haiku.CheckSampling(none, none, true), "top_k")
	require.ErrorContains(t, haiku.CheckSampling(num(1), num(0.99), false), "together")

	sonnet, _ := Claude55("claude-sonnet-5-5")
	require.NoError(t, sonnet.CheckSampling(num(1), num(0.99), false), "Sonnet 5.5 keeps its existing rule")
	require.NoError(t, sonnet.CheckSampling(none, num(1), false))
	opus, _ := Claude55("claude-opus-5-5")
	require.NoError(t, opus.CheckSampling(num(0.2), num(0.5), true), "Opus 5.5 sampling stays with the upstream")
}
