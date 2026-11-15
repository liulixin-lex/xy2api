package apicompat

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChatReasoningUnion(t *testing.T) {
	for _, tc := range []struct{ raw, legacy, effort, summary string }{
		{`null`, "", "", ""}, {`"high"`, "", "high", ""},
		{`{"effort":"low","summary":"auto"}`, "high", "high", "auto"},
		{`"high"`, "high", "high", ""}, {`"high"`, "low", "low", ""},
		{`{"effort":12,"summary":false,"unknown":"ignored"}`, "", "", ""},
		{`[]`, "", "", ""}, {`false`, "", "", ""}, {`42`, "", "", ""},
	} {
		t.Run(tc.raw+tc.legacy, func(t *testing.T) {
			raw := `{"model":"test","reasoning":` + tc.raw + `,"reasoning_effort":"` + tc.legacy + `","n":1,"metadata":{"a":1},"user":"u","max_output_tokens":23,"max_tokens":24,"max_completion_tokens":25,"text":{"verbosity":"high"}}`
			var req ChatCompletionsRequest
			require.NoError(t, json.Unmarshal([]byte(raw), &req))
			require.Equal(t, tc.effort, req.ReasoningEffort)
			got, err := decodeChatReasoning(req.Reasoning, req.ReasoningEffort)
			require.NoError(t, err)
			if tc.effort == "" && tc.summary == "" {
				require.Nil(t, got)
			} else {
				require.Equal(t, &ResponsesReasoning{Effort: tc.effort, Summary: tc.summary}, got)
			}
			require.JSONEq(t, `{"a":1}`, string(req.Metadata))
			require.Equal(t, `"u"`, string(req.User))
			require.Equal(t, 23, *req.MaxOutputTokens)
			require.Equal(t, 24, *req.MaxTokens)
			require.Equal(t, 25, *req.MaxCompletionTokens)
			require.Equal(t, "high", req.Text.Verbosity)
			require.Equal(t, "1", string(req.anthropicN))
			var responses ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(`{"input":"hi","metadata":{"a":1},"user":"u","max_tokens":24,"max_completion_tokens":25}`), &responses))
			require.JSONEq(t, `{"a":1}`, string(responses.Metadata))
			require.Equal(t, "25", string(responses.anthropicAliases.MaxCompletionTokens))
		})
	}
}

func TestChatReasoningWithLegacyExtensionTypes(t *testing.T) {
	for _, raw := range []string{`"high"`, `{"effort":"low"}`, `false`} {
		var req ChatCompletionsRequest
		body := `{"model":"test","reasoning":` + raw + `,"reasoning_effort":"medium","verbosity":12,"text":{"verbosity":false},"max_output_tokens":"invalid"}`
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		require.Equal(t, "medium", req.ReasoningEffort)
		require.Empty(t, req.Verbosity)
		require.Nil(t, req.Text)
		require.Nil(t, req.MaxOutputTokens)
	}
}
