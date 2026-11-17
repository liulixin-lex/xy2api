package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
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

func TestM8GatewayMetadataFreeze(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes []GatewayRequestScope
		want   GatewayRequestScope
		text   bool
	}{
		{"legacy", nil, GatewayRequestScopeText, true},
		{"text", []GatewayRequestScope{GatewayRequestScopeText}, GatewayRequestScopeText, true},
		{"image", []GatewayRequestScope{GatewayRequestScopeImage}, GatewayRequestScopeImage, false},
		{"embedding", []GatewayRequestScope{GatewayRequestScopeEmbedding}, GatewayRequestScopeEmbedding, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRequestState()
			at := time.Now().Add(-time.Second)
			s.InitDelivery(context.Background(), at, nil)
			s.EnableGatewayRequest(time.Now(), tc.scopes...)
			id := s.requestID
			s.NoteGatewaySend(7)
			require.True(t, s.SetGatewayResponseFacts(7, true, true, "upstream_json", false, false, "json"))
			zero := 0
			s.NoteGatewayAttempt(true, "", &zero, nil, nil)
			ctx := WithRequestState(context.Background(), s)
			notice := apicompat.ConversionNotice{Field: "stop", Action: "omit"}
			RecordConversionNotices(ctx, notice)
			s.EnableGatewayRequest(time.Now(), GatewayRequestScopeEmbedding)
			scope, exists := s.GatewayRequestScope()
			require.True(t, exists)
			require.Equal(t, tc.want, scope)
			require.Equal(t, tc.text, s.GatewayRequestEnabled())
			require.Equal(t, at, s.gatewayRecord.started)
			require.Equal(t, 1, s.gatewayRecord.sends)
			s.ExcludeGatewayRequest()
			scope, exists = s.GatewayRequestScope()
			require.True(t, exists)
			if tc.text {
				require.Empty(t, scope)
			} else {
				require.Equal(t, tc.want, scope)
			}
			require.False(t, s.GatewayRequestEnabled())
			s.EnableGatewayRequest(time.Now())
			require.False(t, s.GatewayRequestEnabled(), "repeated Enable must not undo exclusion")
			meta, write := s.ClaimGatewayRequest(false)
			require.Equal(t, !tc.text, write, "excluded text keeps correlation without creating a statistics row")
			require.Equal(t, id, meta.RequestID)
			require.Equal(t, scope, meta.RequestScope)
			require.Equal(t, at, meta.StartedAt)
			require.Equal(t, 0, *meta.InputTokens)
			require.True(t, *meta.UsageObserved)
			require.Equal(t, "upstream_json", *meta.UsageSource)
			require.False(t, *meta.TerminalEventSeen)
			require.False(t, *meta.TerminalEventValid)
			require.Equal(t, "json", *meta.ResponseBodyType)
			require.Equal(t, []apicompat.ConversionNotice{notice}, meta.ConversionNotices)
			require.False(t, s.SetGatewayResponseFacts(7, true, false, "missing", false, false, "json"))
			RecordConversionNotices(ctx, apicompat.ConversionNotice{Field: "other", Action: "omit"})
			*meta.UsageObserved = false
			*meta.UsageSource = "changed"
			*meta.TerminalEventSeen = true
			*meta.TerminalEventValid = true
			*meta.ResponseBodyType = "sse"
			meta.ConversionNotices[0].Field = "changed"
			*meta.InputTokens = 99
			frozen, write := s.ClaimGatewayRequest(false)
			require.False(t, write)
			require.True(t, *frozen.UsageObserved)
			require.Equal(t, "upstream_json", *frozen.UsageSource)
			require.False(t, *frozen.TerminalEventSeen)
			require.False(t, *frozen.TerminalEventValid)
			require.Equal(t, "json", *frozen.ResponseBodyType)
			require.Equal(t, 0, *frozen.InputTokens)
			require.Equal(t, []apicompat.ConversionNotice{notice}, frozen.ConversionNotices)
		})
	}
	t.Run("unknown_missing_and_new_attempt", func(t *testing.T) {
		for _, kind := range []string{"unknown", "missing", "same_account", "different_account"} {
			t.Run(kind, func(t *testing.T) {
				s := NewRequestState()
				s.EnableGatewayRequest(time.Now())
				s.NoteGatewaySend(7)
				if kind != "unknown" {
					require.True(t, s.SetGatewayResponseFacts(7, true, false, "missing", false, false, "json"))
				}
				switch kind {
				case "same_account":
					s.NoteGatewaySend(7)
				case "different_account":
					s.NoteGatewaySend(8)
				}
				meta, _ := s.ClaimGatewayRequest(false)
				if kind == "missing" {
					require.NotNil(t, meta.UsageObserved)
					require.False(t, *meta.UsageObserved)
					require.Equal(t, "missing", *meta.UsageSource)
				} else {
					require.Nil(t, meta.UsageObserved)
					require.Nil(t, meta.UsageSource)
					require.Nil(t, meta.TerminalEventSeen)
					require.Nil(t, meta.TerminalEventValid)
					require.Nil(t, meta.ResponseBodyType)
				}
			})
		}
	})
	t.Run("invalid_and_unadmitted", func(t *testing.T) {
		var absent *RequestState
		absent.EnableGatewayRequest(time.Now(), GatewayRequestScopeText)
		absent.ExcludeGatewayRequest()
		require.False(t, absent.GatewayRequestEnabled())
		meta, write := absent.ClaimGatewayRequest(false)
		require.Nil(t, meta)
		require.False(t, write)
		for _, scopes := range [][]GatewayRequestScope{{""}, {"other"}, {GatewayRequestScopeText, GatewayRequestScopeImage}} {
			s := NewRequestState()
			s.EnableGatewayRequest(time.Now(), scopes...)
			_, exists := s.GatewayRequestScope()
			require.False(t, exists)
			s.ExcludeGatewayRequest()
			_, exists = s.GatewayRequestScope()
			require.False(t, exists)
			s.EnableGatewayRequest(time.Now())
			s.EnableGatewayRequest(time.Now(), scopes...)
			scope, exists := s.GatewayRequestScope()
			require.True(t, exists)
			require.Equal(t, GatewayRequestScopeText, scope)
		}
	})
	t.Run("billing_claim_stays_unique", func(t *testing.T) {
		s := NewRequestState()
		s.EnableGatewayRequest(time.Now(), GatewayRequestScopeImage)
		first, write := s.ClaimGatewayRequest(true)
		require.False(t, write)
		s.ExcludeGatewayRequest()
		s.EnableGatewayRequest(time.Now(), GatewayRequestScopeText)
		second, write := s.ClaimGatewayRequest(false)
		require.False(t, write)
		require.Equal(t, first, second)
	})
}

func TestGatewayRequestRescueRequiresEarlierFailedSend(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sends    int
		failedAt int
		want     int16
	}{
		{"single contradictory capture", 1, 1, RequestSuccess},
		{"two successful sends", 2, 0, RequestSuccess},
		{"only last capture failed", 2, 2, RequestSuccess},
		{"earlier send failed", 2, 1, RequestRescued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewRequestState()
			st.EnableGatewayRequest(time.Now())
			id := st.GatewayRequestID()
			for i := 1; i <= tc.sends; i++ {
				st.NoteGatewaySend(1)
				st.NoteGatewayAttempt(i != tc.failedAt, "synthetic", nil, nil, nil)
			}
			st.CompleteGatewayForward(true, false)
			meta, write := st.ClaimGatewayRequest(true)
			require.False(t, write)
			require.Equal(t, tc.want, meta.Result)
			require.Equal(t, id, meta.RequestID)
			require.NotEmpty(t, meta.RequestID)
			st.NoteGatewayAttempt(false, "late cleanup", nil, nil, nil)
			frozen, _ := st.ClaimGatewayRequest(false)
			require.Equal(t, meta, frozen)
		})
	}
}

func TestGatewaySendClearsPreviousHTTPStatus(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		st := NewRequestState()
		if enabled {
			st.EnableGatewayRequest(time.Now())
		}
		st.NoteUpstreamHTTPStatus(1, 502)
		require.Equal(t, 502, st.UpstreamHTTPStatus(1))
		st.NoteGatewaySend(2)
		require.Zero(t, st.UpstreamHTTPStatus(0))
		st.NoteUpstreamHTTPStatus(2, 200)
		require.Zero(t, st.UpstreamHTTPStatus(1))
		require.Equal(t, 200, st.UpstreamHTTPStatus(2))
		st.NoteGatewaySend(2)
		require.Zero(t, st.UpstreamHTTPStatus(2))
	}
}

func TestGatewayRequestDeliveredIncompleteIsNotRescued(t *testing.T) {
	st := NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.NoteGatewaySend(1)
	st.NoteGatewayAttempt(false, "synthetic_quality_failure", nil, nil, nil)
	st.CompleteGatewayForward(true, false)
	meta, _ := st.ClaimGatewayRequest(true)
	require.Equal(t, RequestSuccess, meta.Result)
	require.Equal(t, "synthetic_quality_failure", meta.ErrorClass)
}

func TestGatewayAttemptKeepsItsRequestIdentity(t *testing.T) {
	st := NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.NoteGatewaySend(1)
	previous := st.GatewayRequestID()
	st.BeginGatewayTurn(time.Now())
	st.NoteGatewaySend(1)
	usage := 7
	st.NoteGatewayAttemptForRequest(previous, false, "synthetic_quality_failure", &usage, nil, nil)
	st.CompleteGatewayForward(true, false)
	meta, _ := st.ClaimGatewayRequest(true)
	require.Equal(t, RequestSuccess, meta.Result)
	require.Empty(t, meta.ErrorClass)
	require.Nil(t, meta.InputTokens)
}
