//go:build unit

package dto

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollabObservationIsAdminOnly(t *testing.T) {
	row := &service.UsageLog{CollabObservation: resilience.EncodeCollabObservation(&resilience.CollabObservation{StopReason: resilience.CollabReasonWriteFailed})}
	user, err := json.Marshal(UsageLogFromService(row))
	require.NoError(t, err)
	require.NotContains(t, string(user), "collab_observation")
	admin := UsageLogFromServiceAdmin(row)
	require.NotEmpty(t, admin.CollabObservation)
	row.CollabObservation[0] = 'x'
	raw, err := json.Marshal(admin)
	require.NoError(t, err)
	require.Contains(t, string(raw), "collab_observation")
}

func TestCollabObservationAdminExpandsSparseV3(t *testing.T) {
	row := &service.UsageLog{CollabObservation: json.RawMessage(`{"v":3,"sends":[{"seq":1,"call_count":0},{"seq":2}]}`)}
	admin := UsageLogFromServiceAdmin(row)
	var got resilience.CollabObservation
	require.NoError(t, json.Unmarshal(admin.CollabObservation, &got))
	require.Equal(t, 2, got.V)
	require.NotNil(t, got.Sends[0].CallCount)
	require.Zero(t, *got.Sends[0].CallCount)
	require.Nil(t, got.Sends[1].CallCount)
	require.Contains(t, string(admin.CollabObservation), `"replacements":{"task":0`)
	require.Contains(t, string(admin.CollabObservation), `"call_count":null`)
	user, err := json.Marshal(UsageLogFromService(row))
	require.NoError(t, err)
	require.NotContains(t, string(user), "collab_observation")
}
