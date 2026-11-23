//go:build unit

package resilience

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabChildDeliveryAndSuccessAdoption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent := NewRequestState()
	var bytes atomic.Int64
	parent.InitDelivery(ctx, time.Now(), bytes.Load)
	parent.SetEdge("synthetic-edge")
	parent.MarkTurnFailed()
	parent.MarkAccountFailed(7)
	parentCtx := WithRequestState(ctx, parent)
	SetRetryAdvice(parentCtx, RetryAdvice{Suppress: true, Source: "non_retryable"})
	child := NewCollabChildState(parent)
	require.Equal(t, parent.Edge(), child.Edge())
	require.False(t, parent.AdoptCollabDelivery(7, child))
	require.True(t, RetryAdviceFrom(parentCtx).Suppress)
	bytes.Store(10)
	parent.NoteClientOutputBytes(10)
	parent.NoteSemanticOutput(time.Now())
	require.Equal(t, int64(10), child.DeliveredBytes())
	require.Equal(t, int64(10), child.ClientOutputBytes())
	require.Equal(t, parent.SemanticOutputAt(), child.SemanticOutputAt())
	require.True(t, parent.AdoptCollabDelivery(7, child))
	require.False(t, RetryAdviceFrom(parentCtx).Suppress)
	require.False(t, parent.TurnFailed())
	require.False(t, parent.AccountFailed(7))
	cancel()
	require.True(t, child.ClientGone())
	require.True(t, child.ClientCanceled())
	a, b, c := child.ResolveDisconnect()
	x, y, z := parent.ResolveDisconnect()
	require.Equal(t, []bool{x, y, z}, []bool{a, b, c})
}

func TestCollabDeliveredSettlementKeepsAccountEffectsDisabled(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		attempt := CollabAttempt{Optional: true, Delivered: delivered}
		require.False(t, attempt.AccountEffectsAllowed())
		require.Equal(t, delivered, attempt.CustomerSettlementAllowed())
		attempt.Abandoned = true
		require.False(t, attempt.CustomerSettlementAllowed())
	}
}
