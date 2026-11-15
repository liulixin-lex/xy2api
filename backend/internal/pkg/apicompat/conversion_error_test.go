package apicompat

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeResponsesPreservesSmallLimit(t *testing.T) {
	for _, limit := range []int{1, 16, 127, 128} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			request := ResponsesRequest{Model: "gpt-4o", MaxOutputTokens: &limit}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			t.Logf("max_output_tokens input=%d wire=%s", limit, wire["max_output_tokens"])
			require.JSONEq(t, fmt.Sprint(limit), string(wire["max_output_tokens"]))
			require.Equal(t, limit, *request.MaxOutputTokens)
		})
	}
}
