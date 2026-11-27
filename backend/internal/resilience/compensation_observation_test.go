package resilience

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCompensationObservesActualAccountChangeWithoutChangingDispatch(t *testing.T) {
	st := NewRequestState()
	st.SetAPIKey(1)
	st.NoteTurn(2, "fixture-model", "fixture-session")
	st.EnableGatewayRequest(time.Now())
	changes := 0
	st.SetAccountSwitchObserver(func(from, to int64) {
		changes++
		require.EqualValues(t, 11, from)
		require.EqualValues(t, 22, to)
		require.NotEmpty(t, st.TurnKey())
	})
	st.MarkOpsUpstreamSendBoundary(11)
	st.NoteGatewayAttempt(false, "fixture", nil, nil, nil)
	st.MarkOpsUpstreamSendBoundary(22)
	st.MarkOpsUpstreamSendBoundary(22)
	meta, _ := st.ClaimGatewayRequest(true)
	require.Equal(t, 1, changes)
	require.EqualValues(t, 1, meta.Switches)
	require.True(t, meta.HadFailedAttempt)
	require.NotNil(t, meta.AccountChangedAt)
	require.EqualValues(t, 11, *meta.AccountChangedFrom)
	require.Equal(t, TurnKey(1, 2, "fixture-model", "fixture-session"), meta.CompensationKey)
	require.Zero(t, *st.Timing().DispatchCount, "the observer does not manufacture transport starts")
}
