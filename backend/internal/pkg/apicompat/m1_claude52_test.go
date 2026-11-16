package apicompat

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestM1Claude52RequestPolicy(t *testing.T) {
	for _, model := range []string{"claude-fable-5-2", "claude-mythos-5-2", "anthropic/claude-fable-5-2", "global.anthropic.claude-mythos-5-2"} {
		t.Run(model, func(t *testing.T) {
			req := &ResponsesRequest{Model: model, Input: json.RawMessage(`"hi"`)}
			out, err := ResponsesToAnthropicRequest(req)
			require.NoError(t, err)
			require.Equal(t, "adaptive", out.Thinking.Type)
			require.Equal(t, "high", out.OutputConfig.Effort)
			req.Reasoning = &ResponsesReasoning{Effort: "low"}
			out, err = ResponsesToAnthropicRequest(req)
			require.NoError(t, err)
			require.Equal(t, "low", out.OutputConfig.Effort)
			for _, choice := range []string{`"required"`, `{"type":"function","name":"f"}`} {
				req.ToolChoice = json.RawMessage(choice)
				_, err = ResponsesToAnthropicRequest(req)
				var unsupported *UnsupportedConversionError
				require.ErrorAs(t, err, &unsupported)
				require.Equal(t, "tool_choice", unsupported.Field)
			}
		})
	}
	for _, model := range []string{"claude-sonnet-4-6", "claude-fable-5-2-20261109"} {
		out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: model, Input: json.RawMessage(`"hi"`)})
		require.NoError(t, err)
		require.Nil(t, out.Thinking)
	}
}
