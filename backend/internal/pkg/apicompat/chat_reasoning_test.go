package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatReasoningUnionConversion(t *testing.T) {
	for _, tc := range []struct {
		name, raw, legacy, effort, summary string
	}{
		{"string", `"high"`, "", "high", "auto"},
		{"object", `{"effort":"low","summary":"concise"}`, "", "low", "concise"},
		{"summary only", `{"summary":"detailed"}`, "", "", "detailed"},
		{"empty summary", `{"effort":"high","summary":""}`, "", "high", ""},
		{"null summary", `{"effort":"high","summary":null}`, "", "high", "auto"},
		{"legacy priority", `{"effort":"low","summary":"concise"}`, "high", "high", "concise"},
		{"extension", `{"exclude":true}`, "high", "high", "auto"},
		{"wrong members", `{"effort":42,"summary":false}`, "high", "high", "auto"},
		{"ignored members", `{"effort":42,"summary":false}`, "", "", ""},
		{"null", `null`, "", "", ""},
		{"array", `[]`, "", "", ""},
		{"number", `42`, "", "", ""},
		{"boolean", `true`, "high", "high", "auto"},
		{"unknown string", `"future"`, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, decoded := range []bool{false, true} {
				req := ChatCompletionsRequest{Model: "test", Reasoning: json.RawMessage(tc.raw), ReasoningEffort: tc.legacy}
				if decoded {
					body := `{"model":"test","reasoning":` + tc.raw + `,"reasoning_effort":"` + tc.legacy + `"}`
					req = ChatCompletionsRequest{}
					require.NoError(t, json.Unmarshal([]byte(body), &req))
				}
				before, err := json.Marshal(req)
				require.NoError(t, err)
				out, err := ChatCompletionsToResponses(&req)
				require.NoError(t, err)
				if tc.effort == "" && tc.summary == "" {
					require.Nil(t, out.Reasoning)
				} else {
					require.Equal(t, &ResponsesReasoning{Effort: tc.effort, Summary: tc.summary}, out.Reasoning)
				}
				if tc.effort == "" && tc.summary != "" {
					wire, err := json.Marshal(out)
					require.NoError(t, err)
					var body struct {
						Reasoning map[string]string `json:"reasoning"`
					}
					require.NoError(t, json.Unmarshal(wire, &body))
					require.Equal(t, map[string]string{"summary": tc.summary}, body.Reasoning)
				}
				after, err := json.Marshal(req)
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
		})
	}
	var invalid ChatCompletionsRequest
	require.Error(t, json.Unmarshal([]byte(`{"reasoning_effort":42}`), &invalid))
}
