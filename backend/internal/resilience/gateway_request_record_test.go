package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayRequestResultAndActualSwitches(t *testing.T) {
	for _, tc := range []struct {
		name               string
		accounts           []int64
		complete, canceled bool
		result, switches   int16
	}{
		{"no_account", nil, false, false, 3, 0},
		{"success", []int64{1}, true, false, 1, 0},
		{"same_account_retry", []int64{1, 1}, true, false, 2, 0},
		{"switch_then_same_account_retry", []int64{1, 2, 2}, true, false, 2, 1},
		{"post_output_failure", []int64{1}, false, false, 3, 0},
		{"client_cancel", []int64{1}, true, true, 4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewRequestState()
			st.EnableGatewayRequest(time.Now())
			for i, id := range tc.accounts {
				if i > 0 {
					st.NoteGatewayAttempt(false, "TRANSIENT", nil, nil, nil)
				}
				st.NoteGatewaySend(id)
			}
			st.CompleteGatewayForward(tc.complete, tc.canceled)
			meta, write := st.ClaimGatewayRequest(false)
			require.True(t, write)
			require.Equal(t, tc.result, meta.Result)
			require.Equal(t, tc.switches, meta.Switches)
			_, write = st.ClaimGatewayRequest(false)
			require.False(t, write)
		})
	}
}

func TestGatewayRequestBillingOwnershipAndTurnSnapshot(t *testing.T) {
	st := NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetSchedSelectionFlags(1, false, true)
	st.NoteGatewaySend(1)
	st.CompleteGatewayForward(false, false)
	input, read, creation := 10, 20, 30
	st.NoteGatewayAttempt(false, "AUTH", &input, &read, &creation)
	st.SetSchedSelectionFlags(2, true, false) // Selected but never sent: cannot replace last-send flags.
	meta, write := st.ClaimGatewayRequest(true)
	require.False(t, write)
	ctx := WithGatewayRequestMetadata(context.Background(), meta)
	// A failed or delayed bill never creates a second unbilled row.
	_, write = st.ClaimGatewayRequest(false)
	require.False(t, write)
	st.BeginGatewayTurn(time.Now())
	st.NoteGatewaySend(2)
	st.CompleteGatewayForward(true, false)
	second, write := st.ClaimGatewayRequest(false)
	require.True(t, write)
	require.NotEqual(t, meta.RequestID, second.RequestID)
	require.EqualValues(t, 1, second.Result)
	first := GatewayRequestMetadataFromContext(ctx)
	require.EqualValues(t, 3, first.Result)
	require.EqualValues(t, 1, first.AccountID)
	require.Equal(t, 10, *first.InputTokens)
	require.True(t, first.Takeover)
	*first.InputTokens = 999
	require.Equal(t, 10, *GatewayRequestMetadataFromContext(ctx).InputTokens)
}

func TestGatewayRequestDeadlineAndLateCancel(t *testing.T) {
	for _, complete := range []bool{false, true} {
		st := NewRequestState()
		client, cancel := context.WithCancel(context.Background())
		st.InitDelivery(client, time.Now(), nil)
		st.EnableGatewayRequest(time.Now())
		st.CompleteGatewayForward(complete, false)
		cancel()
		meta, _ := st.ClaimGatewayRequest(false)
		if complete {
			require.EqualValues(t, 1, meta.Result)
		} else {
			require.EqualValues(t, 4, meta.Result)
		}
	}
	st := NewRequestState()
	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	st.InitDelivery(deadline, time.Now(), nil)
	st.EnableGatewayRequest(time.Now())
	meta, _ := st.ClaimGatewayRequest(false)
	require.EqualValues(t, 3, meta.Result)
	meta, write := NewRequestState().ClaimGatewayRequest(false)
	require.Nil(t, meta)
	require.False(t, write)
}

func TestGatewayRequestTurnClearsSelectionAndPending(t *testing.T) {
	st := NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetSchedSelectionFlags(7, true, true)
	st.SetStickyChange(new(int))
	st.NoteGatewaySend(7)
	st.CompleteGatewayForward(true, false)
	first, _ := st.ClaimGatewayRequest(true)
	st.BeginGatewayTurn(time.Now())
	require.Nil(t, st.StickyChange())
	st.NoteGatewaySend(7)
	st.CompleteGatewayForward(true, false)
	next, _ := st.ClaimGatewayRequest(true)
	require.NotEqual(t, first.RequestID, next.RequestID)
	require.True(t, next.Fallback, "connection-level fallback standing survives turn reset until refreshed")
	require.False(t, next.Takeover)
	require.True(t, first.Fallback)
	require.True(t, first.Takeover)
}
