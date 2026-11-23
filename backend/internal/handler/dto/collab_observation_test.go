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
