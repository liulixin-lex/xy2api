//go:build unit

package repository

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRatefloorPinnedSchedulerRoundTrip(t *testing.T) {
	var account service.Account
	require.NoError(t, json.Unmarshal([]byte(`{"ID":42,"AccountGroups":[{"AccountID":42,"GroupID":7,"Pinned":true},{"AccountID":42,"GroupID":9,"Pinned":false}]}`), &account))
	slim := buildSchedulerMetadataAccount(account)
	raw, err := json.Marshal(slim)
	require.NoError(t, err)
	var restored service.Account
	require.NoError(t, json.Unmarshal(raw, &restored))
	pinned, ok := any(&restored).(interface{ PinnedIn(int64) bool })
	require.True(t, ok, "scheduler account must expose group-scoped pinning")
	require.True(t, pinned.PinnedIn(7))
	require.False(t, pinned.PinnedIn(9))
	require.False(t, pinned.PinnedIn(0))
	require.False(t, pinned.PinnedIn(11))
	t.Log("group 7 pinned survives scheduler serialization; groups 9/11 unpinned")
}
