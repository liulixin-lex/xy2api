package apicompat

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestM1GeminiIntermediateIntent(t *testing.T) {
	for _, limit := range []int{512, 1024} {
		for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			req := &ResponsesRequest{Model: "gemini-test", MaxOutputTokens: &limit}
			if effort != "" {
				req.Reasoning = &ResponsesReasoning{Effort: effort}
			}
			require.NoError(t, MarkConversionTarget(req, "gemini"))
			out := AnthropicRequest{MaxTokens: limit}
			require.NoError(t, mapAnthropicReasoning(req, &out))
			if effort == "none" {
				require.Equal(t, &AnthropicThinking{Type: "disabled"}, out.Thinking)
				require.Nil(t, out.OutputConfig)
			} else if effort == "" {
				require.Nil(t, out.Thinking)
				require.Nil(t, out.OutputConfig)
			} else {
				require.Nil(t, out.Thinking)
				require.Equal(t, effort, out.OutputConfig.Effort)
			}
		}
	}
	for _, format := range []string{`{"type":"json_object"}`, `{"type":"json_schema","name":"example","strict":false,"schema":{"type":"object","properties":{"n":{"const":9007199254740993}}}}`} {
		req := &ResponsesRequest{Text: &ResponsesText{Format: json.RawMessage(format)}}
		require.NoError(t, MarkConversionTarget(req, "gemini"))
		var out AnthropicRequest
		require.NoError(t, mapAnthropicResponseFormat(req, &out))
		require.Equal(t, format, string(out.OutputConfig.Format))
	}
	req := &ResponsesRequest{Text: &ResponsesText{Format: json.RawMessage(`{"type":"json_object"}`)}}
	var out AnthropicRequest
	require.NoError(t, mapAnthropicResponseFormat(req, &out))
	require.Nil(t, out.OutputConfig)
}
