package resilience

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withPublished(t *testing.T, mutate func(*Settings)) {
	t.Helper()
	orig := *Current()
	s := Defaults()
	if mutate != nil {
		mutate(&s)
	}
	Publish(s)
	t.Cleanup(func() { Publish(orig) })
}

func newDeliveryState(t *testing.T, delivered *atomic.Int64) (*RequestState, context.Context, context.CancelFunc) {
	t.Helper()
	client, cancel := context.WithCancel(context.Background())
	st := NewRequestState()
	st.InitDelivery(client, time.Now(), delivered.Load)
	st.SetAPIKey(42)
	return st, WithRequestState(client, st), cancel
}

func TestResolveDisconnect(t *testing.T) {
	withPublished(t, nil)
	var delivered atomic.Int64

	// Normal end: not a disconnect.
	st, _, cancel := newDeliveryState(t, &delivered)
	st.MarkFinished()
	cancel()
	disconnected, _, _ := st.ResolveDisconnect()
	require.False(t, disconnected)

	// A context that is still alive is not a disconnect either.
	st, _, cancel = newDeliveryState(t, &delivered)
	disconnected, _, _ = st.ResolveDisconnect()
	require.False(t, disconnected)
	cancel()

	// Heartbeats only: undelivered; decided once.
	st, _, cancel = newDeliveryState(t, &delivered)
	st.SetStage(StageHeaderWait)
	cancel()
	disconnected, deliveredOut, abuse := st.ResolveDisconnect()
	require.True(t, disconnected)
	require.False(t, deliveredOut)
	require.False(t, abuse)
	require.True(t, st.Undelivered())
	stage, _ := st.DisconnectInfo()
	require.Equal(t, StageHeaderWait, stage)
	delivered.Store(10)
	_, deliveredOut, _ = st.ResolveDisconnect()
	require.False(t, deliveredOut, "resolved once")
	require.True(t, st.ClaimUndeliveredRecord())
	require.False(t, st.ClaimUndeliveredRecord(), "one record per request")

	// Partial delivery.
	st, _, cancel = newDeliveryState(t, &delivered)
	cancel()
	_, deliveredOut, _ = st.ResolveDisconnect()
	require.True(t, deliveredOut)
	stage, _ = st.DisconnectInfo()
	require.Equal(t, StageStreaming, stage)
	require.False(t, st.ClaimUndeliveredRecord())
}

func TestResolveDisconnectAbuseGuard(t *testing.T) {
	withPublished(t, nil)
	var calls atomic.Int64
	SetUndeliveredAbuseCheck(func(apiKeyID int64) bool {
		require.Equal(t, int64(42), apiKeyID)
		return calls.Add(1) > 30
	})
	t.Cleanup(func() { SetUndeliveredAbuseCheck(nil) })
	var delivered atomic.Int64
	var abused int
	for i := 0; i < 31; i++ {
		st, _, cancel := newDeliveryState(t, &delivered)
		cancel()
		_, _, abuse := st.ResolveDisconnect()
		st.ResolveDisconnect()
		if abuse {
			abused++
		}
	}
	require.Equal(t, int64(31), calls.Load(), "counted once per request")
	require.Equal(t, 1, abused, "the 31st undelivered request is billed")
}

func TestUpstreamContextOnDisconnect(t *testing.T) {
	withPublished(t, func(s *Settings) { s.Disconnect.PartialDrainMaxSeconds = 1 })
	var delivered atomic.Int64

	// Undelivered: cancelled at once.
	st, ctx, cancel := newDeliveryState(t, &delivered)
	upstream := UpstreamContextOnDisconnect(ctx, context.WithoutCancel(ctx))
	cancel()
	select {
	case <-upstream.Done():
	case <-time.After(time.Second):
		t.Fatal("undelivered upstream not cancelled")
	}
	require.True(t, st.Undelivered())

	// Partial: drained for at most partial_drain_max_seconds.
	delivered.Store(100)
	_, ctx, cancel = newDeliveryState(t, &delivered)
	upstream = UpstreamContextOnDisconnect(ctx, context.WithoutCancel(ctx))
	start := time.Now()
	cancel()
	select {
	case <-upstream.Done():
		require.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
	case <-time.After(3 * time.Second):
		t.Fatal("partial drain not bounded")
	}

	// Normal end: never cancelled.
	st, ctx, cancel = newDeliveryState(t, &delivered)
	upstream = UpstreamContextOnDisconnect(ctx, context.WithoutCancel(ctx))
	st.MarkFinished()
	cancel()
	time.Sleep(1200 * time.Millisecond)
	require.NoError(t, upstream.Err())

	// Switch off: the 0.3.0 detached context.
	withPublished(t, func(s *Settings) { s.Disconnect.Enabled = false })
	delivered.Store(0)
	_, ctx, cancel = newDeliveryState(t, &delivered)
	detached := context.WithoutCancel(ctx)
	require.Equal(t, detached, UpstreamContextOnDisconnect(ctx, detached))
	cancel()
}
