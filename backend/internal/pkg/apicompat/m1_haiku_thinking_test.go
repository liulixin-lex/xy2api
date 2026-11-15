package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM1HaikuDisabledThinking(t *testing.T) {
	for _, tc := range []struct {
		model, effort, mode, actual string
	}{
		{"claude-haiku-5-5", "none", "disabled", "low"},
		{"claude-haiku-5-5", "minimal", "adaptive", "low"},
		{"claude-haiku-5-5", "", "adaptive", "medium"},
		{"claude-sonnet-5-5", "none", "between_tools", "low"},
		{"claude-opus-5-5", "none", "adaptive", "low"},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			req := &ResponsesRequest{Model: tc.model, Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: tc.effort}}
			format := json.RawMessage(`{"type":"json_schema","schema":{"type":"object"}}`)
			out := &AnthropicRequest{MaxTokens: 4096, OutputConfig: &AnthropicOutputConfig{Format: format}}
			require.NoError(t, mapAnthropicReasoning(req, out))
			require.Equal(t, tc.mode, out.Thinking.Type)
			require.Equal(t, tc.actual, out.OutputConfig.Effort)
			require.JSONEq(t, string(format), string(out.OutputConfig.Format))
			if tc.model == "claude-haiku-5-5" {
				req.ToolChoice = json.RawMessage(`"required"`)
				converted, err := ResponsesToAnthropicRequest(req)
				require.NoError(t, err)
				require.Equal(t, tc.mode, converted.Thinking.Type)
				require.Equal(t, tc.actual, converted.OutputConfig.Effort)
				require.JSONEq(t, `{"type":"any"}`, string(converted.ToolChoice))
			}
		})
	}
}
