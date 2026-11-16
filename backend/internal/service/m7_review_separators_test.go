//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestM7ReviewChatMissingBlankSeparators(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"raw", "responses", "messages"} {
		t.Run(target, func(t *testing.T) {
			wire := "data: " + m7ReviewChatContent + "\ndata: " + m7ReviewChatFinish + "\ndata: [DONE]\n"
			result, body, err := m7ReviewChatStream(t, target, wire)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.PartialFailure)
			require.True(t, result.TerminalValid)
			require.Contains(t, body, "hello")
		})
	}
}
