package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT61SolAnthropicEffortAndLegacyIsolation(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "openai/GPT_6.1_SOL"} {
		for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
			t.Run(model+"/"+effort, func(t *testing.T) {
				sampling := 0.7
				out, err := AnthropicToResponses(&AnthropicRequest{Model: model, Temperature: &sampling, TopP: &sampling, OutputConfig: &AnthropicOutputConfig{Effort: effort}})
				require.NoError(t, err)
				require.Equal(t, effort, out.Reasoning.Effort)
				require.Nil(t, out.Temperature)
				require.Nil(t, out.TopP)
			})
		}
		for _, effort := range []string{"none", "minimal", " NONE "} {
			_, err := AnthropicToResponses(&AnthropicRequest{Model: model, OutputConfig: &AnthropicOutputConfig{Effort: effort}})
			require.Error(t, err)
		}
		_, err := AnthropicToResponses(&AnthropicRequest{Model: model, Thinking: &AnthropicThinking{Type: "disabled"}, OutputConfig: &AnthropicOutputConfig{Effort: "max"}})
		require.Error(t, err)
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-sol", "gpt-6-luna"} {
		out, err := AnthropicToResponses(&AnthropicRequest{Model: model, Thinking: &AnthropicThinking{Type: "disabled"}, OutputConfig: &AnthropicOutputConfig{Effort: "max"}})
		require.NoError(t, err)
		require.Equal(t, "none", out.Reasoning.Effort, "upstream thinking disable takes precedence for older models")
		out, err = AnthropicToResponses(&AnthropicRequest{Model: model, OutputConfig: &AnthropicOutputConfig{Effort: "max"}})
		require.NoError(t, err)
		require.Equal(t, "xhigh", out.Reasoning.Effort, "older models still map max to xhigh when thinking is enabled")
	}
}

func TestGPT61SolConvertersRejectDisabledModelAliases(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol-none", "openai/GPT_6.1_SOL_MINIMAL"} {
		_, err := AnthropicToResponses(&AnthropicRequest{Model: model})
		require.Error(t, err)
		_, err = ChatCompletionsToResponses(&ChatCompletionsRequest{Model: model})
		require.Error(t, err)
	}
}
