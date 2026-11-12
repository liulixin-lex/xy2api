package apicompat

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Captured with the pristine v0.6.3 converter at e893fef5f4820ef2f18476f20f85121f3984f1f9.
// Compare wire bytes, not JSON equivalence: signed thinking binds system/tools.
func TestAnthropicConversionV063Golden(t *testing.T) {
	raw, err := os.ReadFile("testdata/anthropic_conversion_v063.json")
	require.NoError(t, err)
	var cases []struct {
		Name      string          `json:"name"`
		Entry     string          `json:"entry"`
		Request   json.RawMessage `json:"request"`
		Anthropic string          `json:"anthropic"`
	}
	require.NoError(t, json.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var responses *ResponsesRequest
			if tc.Entry == "chat" {
				var req ChatCompletionsRequest
				require.NoError(t, json.Unmarshal(tc.Request, &req))
				var err error
				responses, err = ChatCompletionsToResponses(&req)
				require.NoError(t, err)
			} else {
				responses = &ResponsesRequest{}
				require.NoError(t, json.Unmarshal(tc.Request, responses))
			}
			out, err := ResponsesToAnthropicRequest(responses)
			require.NoError(t, err)
			wire, err := json.Marshal(out)
			require.NoError(t, err)
			require.Equal(t, tc.Anthropic, string(wire))
		})
	}
}
