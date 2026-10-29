package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOpenAITOTPImportMarkerCannotBeForgedCopiedOrCleared(t *testing.T) {
	key := ManagedOpenAILoginMarkerExtraKey
	forged := map[string]any{key: "guessed-task-id", "public": true}
	extra := managedOpenAICreateExtra(&CreateAccountInput{Extra: forged})
	require.NotContains(t, extra, key)
	require.Equal(t, true, extra["public"])
	require.Contains(t, forged, key, "request maps are not mutated")
	trusted := managedOpenAICreateExtra(&CreateAccountInput{Extra: forged, ManagedOpenAILoginMarker: "random-server-marker"})
	require.Equal(t, "random-server-marker", trusted[key])
	for _, input := range []map[string]any{nil, {}, {key: "forged"}} {
		updated := preserveManagedOpenAIImportMarker(input, trusted)
		require.Equal(t, "random-server-marker", updated[key])
	}
	copied, err := duplicateAccountExtra(trusted)
	require.NoError(t, err)
	require.NotContains(t, copied, key)
	stripped := preserveManagedOpenAIImportMarker(forged, nil)
	require.NotContains(t, stripped, key)
}
