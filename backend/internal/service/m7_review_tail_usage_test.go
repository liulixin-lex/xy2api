//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestM7ReviewTrailingUsageShapes(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"raw", "responses", "messages"} {
		for _, prefix := range []string{"", `"choices":null,`, `"choices":[],`, `"choices":[{"index":0,"delta":{},"finish_reason":null}],`} {
			t.Run(target+prefix, func(t *testing.T) {
				wire := "data: " + m7ReviewChatContent + "\n\ndata: {" + prefix + `"usage":{"prompt_tokens":11,"completion_tokens":6,"total_tokens":17}}` + "\n"
				result, _, err := m7ReviewChatStream(t, target, wire)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.False(t, result.PartialFailure)
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 6, result.Usage.OutputTokens)
				require.False(t, result.TerminalSeen)
				require.False(t, result.TerminalValid)
			})
		}
	}
}
