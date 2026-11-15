package apicompat

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeResponsesPreservesSmallLimit(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{1, 16}, {15, 16}, {16, 16}, {127, 127}, {128, 128}} {
		t.Run(fmt.Sprint(tc.input), func(t *testing.T) {
			limit := tc.input
			request := ResponsesRequest{Model: "gpt-4o", MaxOutputTokens: &limit}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			t.Logf("max_output_tokens input=%d wire=%s", tc.input, wire["max_output_tokens"])
			require.JSONEq(t, fmt.Sprint(tc.want), string(wire["max_output_tokens"]))
			require.Equal(t, tc.input, *request.MaxOutputTokens)
		})
	}
}
