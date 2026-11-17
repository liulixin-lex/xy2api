package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type lateCommitOutboxCache struct {
	outboxCleanupCache
	observed []int64
}

func (c *lateCommitOutboxCache) UpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	for id := range updates {
		c.observed = append(c.observed, id)
	}
	return nil
}

func TestSchedulerOutboxLateCommit(t *testing.T) {
	event := func(id int64) SchedulerOutboxEvent {
		return SchedulerOutboxEvent{ID: id, EventType: SchedulerOutboxEventAccountLastUsed,
			Payload: map[string]any{"last_used": map[string]any{strconv.FormatInt(id, 10): int64(100)}}}
	}
	// Transaction 1 allocates ID 1 and remains uncommitted. Transaction 2
	// commits ID 2 first; only committed rows are exposed to each poll.
	cache := &lateCommitOutboxCache{}
	repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{event(2)}, rows: []int64{2}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
	svc.pollOutbox()
	require.Equal(t, []int64{2}, cache.observed)
	// Transaction 1 commits after processing ID 2. There is no timing/sleep.
	repo.events = append(repo.events, event(1))
	repo.rows = append(repo.rows, 1)
	svc.pollOutbox()
	require.Equal(t, []int64{2, 1}, cache.observed)
	svc.pollOutbox()
	require.Equal(t, []int64{2, 1}, cache.observed, "acknowledged events must not replay")
}

// failureReplayCache makes a partial batch visible and then fails its next
// write. Assigning the same last-used projection twice has one final effect.
type failureReplayCache struct {
	outboxCleanupCache
	last    map[int64]time.Time
	calls   int
	failAt  int
	started chan struct{}
	resume  chan struct{}
}

func (c *failureReplayCache) UpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	c.calls++
	if c.started != nil {
		close(c.started)
		<-c.resume
		c.started = nil
	}
	if c.calls == c.failAt {
		return errors.New("synthetic cache failure")
	}
	for id, at := range updates {
		if at.After(c.last[id]) {
			c.last[id] = at
		}
	}
	return nil
}
func TestSchedulerOutboxPartialBatchReplayAndConsumerExclusion(t *testing.T) {
	event := func(id int64) SchedulerOutboxEvent {
		return SchedulerOutboxEvent{ID: id, EventType: SchedulerOutboxEventAccountLastUsed,
			Payload: map[string]any{"last_used": map[string]any{strconv.FormatInt(id, 10): int64(100)}}}
	}
	cache := &failureReplayCache{last: make(map[int64]time.Time), failAt: 2, started: make(chan struct{}), resume: make(chan struct{})}
	repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{event(1), event(2)}, rows: []int64{1, 2}}
	first := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
	second := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
	started, resume := cache.started, cache.resume
	done := make(chan struct{})
	go func() { first.pollOutbox(); close(done) }()
	<-started
	second.pollOutbox() // cannot enter while the first consumer holds its lease
	require.Equal(t, 1, cache.calls)
	close(resume)
	<-done
	require.Len(t, repo.events, 1, "only the failed event remains pending")
	require.Equal(t, int64(2), repo.events[0].ID)
	require.Equal(t, map[int64]time.Time{1: time.Unix(100, 0)}, cache.last)
	second.pollOutbox() // replay after failure or process restart
	require.Empty(t, repo.events)
	require.Equal(t, map[int64]time.Time{1: time.Unix(100, 0), 2: time.Unix(100, 0)}, cache.last)
	require.Equal(t, 3, cache.calls, "the completed projection must not replay")
	first.pollOutbox()
	require.Equal(t, 3, cache.calls)
}
