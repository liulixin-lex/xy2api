package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupSystemPromptResolution(t *testing.T) {
	c, err := NormalizeGroupSystemPromptConfig(GroupSystemPromptConfig{Prompt: "common", Scope: "selected", Models: []string{" alias ", "alias"}, ModelPrompts: map[string]string{" other ": "specific", "alias": "  "}})
	require.NoError(t, err)
	for model, want := range map[string]string{" alias ": "common", "other": "specific", "Alias": "", "upstream": "", "": ""} {
		require.Equal(t, want, c.Resolve(model), model)
	}
	clone := c.Clone()
	clone.Models[0] = "changed"
	clone.ModelPrompts["other"] = "changed"
	require.Equal(t, "common", c.Resolve("alias"))
	require.Equal(t, "specific", c.Resolve("other"))
	for _, invalid := range []GroupSystemPromptConfig{
		{Scope: "unknown"}, {Prompt: "policy", Scope: "selected"},
		{Models: []string{" "}}, {ModelPrompts: map[string]string{"alias": "a", " alias ": "b"}},
	} {
		_, err := NormalizeGroupSystemPromptConfig(invalid)
		require.Error(t, err)
	}
	cleared, err := NormalizeGroupSystemPromptConfig(GroupSystemPromptConfig{})
	require.NoError(t, err)
	require.Equal(t, "all", cleared.Scope)
	require.Empty(t, cleared.Resolve("alias"))
}
