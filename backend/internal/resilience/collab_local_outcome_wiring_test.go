//go:build unit

package resilience

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabLocalOutcomeBeforeFreeze(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		s := NewRequestState()
		require.True(t, s.SetCollabState(&CollabRequestState{}))
		s.EnableGatewayRequest(time.Now())
		s.NoteGatewaySend(1)
		s.NoteGatewayAttempt(false, "cipher_rejected", nil, nil, nil)
		s.NoteCollabLocalOutcome(delivered)
		metadata, unbilled := s.ClaimGatewayRequest(false)
		require.True(t, unbilled)
		want := RequestLocalUndeliverable
		if delivered {
			want = RequestLocalEnded
		}
		require.Equal(t, want, metadata.Result)
		require.Equal(t, "cipher_rejected", metadata.ErrorClass)
		s.NoteCollabLocalOutcome(!delivered)
		frozen, second := s.ClaimGatewayRequest(false)
		require.False(t, second)
		require.Equal(t, want, frozen.Result)
	}
}
