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
		{"text_legacy", GatewayRequestScopeText, true, true, false, false},
		{"image_legacy", GatewayRequestScopeImage, true, true, false, false},
		{"embedding_legacy", GatewayRequestScopeEmbedding, true, true, false, false},
		{"pending_intent_no_decision", GatewayRequestScopeSearch, true, false, false, false},
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
