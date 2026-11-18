package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayRequestWaiverTailScopeAndEvidence(t *testing.T) {
	previous := *Current()
	Publish(Defaults())
	t.Cleanup(func() { Publish(previous) })
	for _, tc := range []struct {
		name                                string
		scope                               GatewayRequestScope
		disconnect, usage, billed, wantTail bool
	}{
		{"search_waiver", GatewayRequestScopeSearch, true, true, false, true},
		{"text_explicit_waiver", GatewayRequestScopeText, true, true, false, true},
		{"image_legacy", GatewayRequestScopeImage, true, true, false, false},
		{"embedding_legacy", GatewayRequestScopeEmbedding, true, true, false, false},
		{"pending_intent_no_decision", GatewayRequestScopeSearch, true, false, false, false},
		{"text_bill_already_owned", GatewayRequestScopeText, true, true, true, false},
		{"text_persistence_failure_without_waiver", GatewayRequestScopeText, true, false, false, false},
		{"zero_amount_or_abuse_billed", GatewayRequestScopeSearch, true, true, true, false},
		{"usage_without_disconnect", GatewayRequestScopeSearch, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := NewRequestState()
			st.InitDelivery(ctx, time.Now(), func() int64 { return 0 })
			st.EnableGatewayRequest(time.Now(), tc.scope)
			st.NoteGatewaySend(42)
			if tc.disconnect {
				cancel()
				st.ResolveDisconnect()
			}
			st.CompleteGatewayForward(false, tc.disconnect)
			before, write := st.ClaimGatewayRequest(true)
			require.False(t, write)
			if tc.usage {
				st.NoteUndeliveredUsage([]byte(`{"input_tokens":1}`), 0, tc.billed)
			}
			after, write := st.ClaimGatewayRequest(false)
			require.Equal(t, tc.wantTail, write)
			require.Equal(t, before, after, "ownership transfer must preserve the frozen identity and outcome")
			_, write = st.ClaimGatewayRequest(false)
			require.False(t, write, "a waiver transfers only once")
		})
	}
}

func TestGatewayRequestLateWaiverResult(t *testing.T) {
	previous := *Current()
	Publish(Defaults())
	t.Cleanup(func() { Publish(previous) })
	for _, tc := range []struct {
		name                     string
		scope                    GatewayRequestScope
		waived, billed, wantTail bool
	}{
		{"search", GatewayRequestScopeSearch, true, false, true},
		{"text_explicit_waiver", GatewayRequestScopeText, true, false, true},
		{"no_waiver", GatewayRequestScopeSearch, false, false, false},
		{"billing_intent", GatewayRequestScopeSearch, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := NewRequestState()
			st.InitDelivery(ctx, time.Now(), func() int64 { return 0 })
			st.EnableGatewayRequest(time.Now(), tc.scope)
			st.SetSchedSelectionFlags(42, true, true)
			st.NoteGatewaySend(42)
			input, read, creation := 17, 3, 2
			st.NoteGatewayAttempt(false, "write_failed", &input, &read, &creation)
			st.CompleteGatewayForward(false, false)
			before, write := st.ClaimGatewayRequest(true)
			require.False(t, write)
			require.Equal(t, RequestFailed, before.Result)
			cancel()
			st.ResolveDisconnect()
			if tc.waived {
				st.NoteUndeliveredUsage([]byte(`{"input_tokens":17}`), 0, tc.billed)
			}
			after, write := st.ClaimGatewayRequest(false)
			require.Equal(t, tc.wantTail, write)
			expected := *before
			if tc.wantTail {
				expected.Result = RequestCanceled
			}
			require.Equal(t, &expected, after, "only the explicitly waived tail result may change")
			require.Equal(t, RequestFailed, before.Result, "the billing caller's frozen copy stays immutable")
			_, write = st.ClaimGatewayRequest(false)
			require.False(t, write)
		})
	}
}

// Billing can freeze a text failure before it learns of the disconnect.
// Multiple tail callers racing after the waiver still own just one canceled row.
func TestTextWaiverHasOneCanceledRowOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := NewRequestState()
	st.InitDelivery(ctx, time.Now(), func() int64 { return 0 })
	st.EnableGatewayRequest(time.Now(), GatewayRequestScopeText)
	st.NoteGatewaySend(7)
	st.CompleteGatewayForward(false, false)
	bill, write := st.ClaimGatewayRequest(true)
	require.False(t, write)
	cancel()
	st.ResolveDisconnect()
	st.NoteUndeliveredUsage([]byte(`{"input_tokens":7}`), 0, false)
	owners := make(chan *GatewayRequestMetadata, 2)
	for range 2 {
		go func() {
			meta, won := st.ClaimGatewayRequest(false)
			if !won {
				meta = nil
			}
			owners <- meta
		}()
	}
	var rows []*GatewayRequestMetadata
	for range 2 {
		if meta := <-owners; meta != nil {
			rows = append(rows, meta)
		}
	}
	require.Len(t, rows, 1)
	require.Equal(t, RequestCanceled, rows[0].Result)
	require.Equal(t, GatewayRequestScopeText, rows[0].RequestScope)
	require.NotEmpty(t, bill.RequestID)
	require.Equal(t, bill.RequestID, rows[0].RequestID)
}

func TestWebSocketWaiverPreservesAdmittedScopeAcrossTurns(t *testing.T) {
	for _, scope := range []GatewayRequestScope{GatewayRequestScopeText, GatewayRequestScopeImage} {
		for _, waivedTurn := range []int{1, 2} {
			ctx, cancel := context.WithCancel(context.Background())
			st := NewRequestState()
			st.InitDelivery(ctx, time.Now(), func() int64 { return 0 })
			st.EnableGatewayRequest(time.Now(), scope)
			for turn := 1; turn <= waivedTurn; turn++ {
				st.BeginGatewayTurn(time.Now())
				st.NoteGatewaySend(42)
				st.CompleteGatewayForward(turn != waivedTurn, false)
				bill, _ := st.ClaimGatewayRequest(true)
				require.Equal(t, scope, bill.RequestScope)
				if turn == waivedTurn {
					cancel()
					st.ResolveDisconnect()
					st.NoteUndeliveredUsage([]byte(`{"input_tokens":3}`), 0, false)
					meta, write := st.ClaimGatewayRequest(false)
					require.Equal(t, scope == GatewayRequestScopeText, write)
					if write {
						require.Equal(t, RequestCanceled, meta.Result)
						require.Equal(t, bill.RequestID, meta.RequestID)
					}
				}
				_, write := st.ClaimGatewayRequest(false)
				require.False(t, write)
			}
			cancel()
		}
	}
}

func TestWebSocketTextWaiverAfterBinaryExcludedTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := NewRequestState()
	st.InitDelivery(ctx, time.Now(), func() int64 { return 0 })
	st.EnableGatewayRequest(time.Now(), GatewayRequestScopeText)
	st.BeginGatewayTurn(time.Now())
	st.ExcludeGatewayRequest()
	first, write := st.ClaimGatewayRequest(false)
	require.False(t, write)
	require.Empty(t, first.RequestScope)
	st.BeginGatewayTurn(time.Now())
	st.NoteGatewaySend(1)
	bill, _ := st.ClaimGatewayRequest(true)
	cancel()
	st.ResolveDisconnect()
	st.NoteUndeliveredUsage([]byte(`{"input_tokens":2}`), 0, false)
	tail, write := st.ClaimGatewayRequest(false)
	require.True(t, write)
	require.Equal(t, GatewayRequestScopeText, tail.RequestScope)
	require.Equal(t, RequestCanceled, tail.Result)
	require.Equal(t, bill.RequestID, tail.RequestID)
	require.NotEqual(t, first.RequestID, tail.RequestID)
	_, write = st.ClaimGatewayRequest(false)
	require.False(t, write)
}
