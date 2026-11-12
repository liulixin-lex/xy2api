//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Preserve the G3 interleaving: B reads history before A starts, then resumes after A finishes.
type g3DelayedLatest struct {
	*schedTestMemoryStore
	read    chan struct{}
	release chan struct{}
}

func (s *g3DelayedLatest) Latest(ctx context.Context, id int64, model string) (*SchedTestRecord, error) {
	last, err := s.schedTestMemoryStore.Latest(ctx, id, model)
	close(s.read)
	<-s.release
	return last, err
}

func TestG3CrossInstanceRetryWindow(t *testing.T) {
	a, shared, forwardA, _, _ := schedTestFixture(10)
	b, _, forwardB, _, _ := schedTestFixture(10)
	defer a.Stop()
	defer b.Stop()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	a.now, b.now = func() time.Time { return now }, func() time.Time { return now }
	a.wait, b.wait = func(context.Context, time.Duration) bool { return true }, func(context.Context, time.Duration) bool { return true }
	var sends atomic.Int64
	forward := func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
		sends.Add(1)
		return schedTestOK(ctx, 1), nil
	}
	forwardA.Fn, forwardB.Fn = forward, forward
	delayed := &g3DelayedLatest{shared, make(chan struct{}), make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(delayed.release) })
	b.store = delayed
	second := make(chan SchedTestTicket, 1)
	secondErr := make(chan error, 1)
	go func() {
		ticket, err := b.Request(context.Background(), 7, "evidence", false, 42)
		second <- ticket
		secondErr <- err
	}()
	<-delayed.read
	first, err := a.Request(context.Background(), 7, "evidence", false, 42)
	require.NoError(t, err)
	require.True(t, first.Started)
	firstRecord := <-shared.finished
	require.True(t, firstRecord.Done)
	release.Do(func() { close(delayed.release) })
	secondTicket := <-second
	require.NoError(t, <-secondErr)
	require.False(t, secondTicket.Started, "atomic admission must reject the stale Latest result")
	require.Equal(t, "too_soon", secondTicket.Why)
	require.Empty(t, secondTicket.TestID)
	require.Equal(t, int64(9), sends.Load())
	latest, err := shared.Latest(context.Background(), 7, "evidence")
	require.NoError(t, err)
	require.Equal(t, firstRecord.ID, latest.ID)
	t.Logf("one test started at %s; stale second request=%s; simulated upstream sends=%d", now.Format(time.RFC3339), secondTicket.Why, sends.Load())
}
