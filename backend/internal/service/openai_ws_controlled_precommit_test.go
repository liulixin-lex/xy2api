package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamPassthroughGuardCommitsBeforeFailedWrite(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)
	c := newOpenAIWSControlledPassthroughFrameConn(ctx, nil, nil)
	c.active = d
	require.NoError(t, c.tryCommitOutput([]byte(`{"type":"response.created","response":{"id":"resp_original"}}`)))
	require.True(t, r.Ledger.Snapshot().Committed)
	require.True(t, ControlledStreamSnapshot(ctx).AttemptCommitted)
	require.False(t, ControlledStreamSnapshot(ctx).SemanticSeen)
	require.ErrorIs(t, r.Ledger.BeginAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted, "a following downstream write failure cannot admit another generation")
}

func TestNativeStreamPassthroughGuardTimeoutRace(t *testing.T) {
	for i := 0; i < 250; i++ {
		ctx, r, d := nativeCommitFixture(t)
		c := newOpenAIWSControlledPassthroughFrameConn(ctx, nil, nil)
		c.active = d
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var err error
		go func() {
			defer wg.Done()
			<-start
			err = c.tryCommitOutput([]byte(`{"type":"response.created"}`))
		}()
		go func() {
			defer wg.Done()
			<-start
			d.mu.Lock()
			d.timeout = true
			d.cancellationReason = ControlledStartupTimeout
			d.cancel()
			d.mu.Unlock()
		}()
		close(start)
		wg.Wait()
		if err == nil {
			require.ErrorIs(t, r.Ledger.BeginAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
		} else {
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.False(t, r.Ledger.Snapshot().Committed)
		}
	}
}

func TestNativeStreamPassthroughGuardPreservesLegacyDouble(t *testing.T) {
	c := newOpenAIWSControlledPassthroughFrameConn(context.Background(), nil, nil)
	d := newControlledPassthroughTestAttempt()
	c.active = d
	defer d.cancel()
	require.NoError(t, c.tryCommitOutput([]byte(`{"type":"response.created"}`)))
	_, _, committed, _ := d.counts()
	require.Equal(t, 1, committed)
	c.cancel(context.Canceled)
	require.ErrorIs(t, c.tryCommitOutput([]byte(`{"type":"response.created"}`)), context.Canceled)
	_, _, committed, _ = d.counts()
	require.Equal(t, 1, committed)
}

func TestNativeStreamPassthroughGuardObserveOnlyRequest(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	c := newOpenAIWSControlledPassthroughFrameConn(ctx, nil, nil)
	c.requestCtx = ctx
	require.NoError(t, c.tryCommitOutput([]byte(`{"type":"response.created"}`)))
	require.True(t, r.Ledger.Snapshot().Committed)
	require.False(t, ControlledStreamSnapshot(ctx).SemanticSeen)
}
