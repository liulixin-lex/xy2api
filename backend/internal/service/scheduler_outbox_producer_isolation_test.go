package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Model the database's dedup-key ownership: an autocommit read releases the
// key immediately; a producer waiting for the key reports that explicitly.
type producerIsolationRepo struct {
	outboxCleanupRepo
	keyReleased chan struct{}
	releaseKey  sync.Once
}

func (r *producerIsolationRepo) releaseDedupKey() { r.releaseKey.Do(func() { close(r.keyReleased) }) }
func (r *producerIsolationRepo) ListPendingAndReleaseDedup(ctx context.Context, limit int) ([]SchedulerOutboxEvent, error) {
	events, err := r.outboxCleanupRepo.ListPendingAndReleaseDedup(ctx, limit)
	r.releaseDedupKey()
	return events, err
}
func (r *producerIsolationRepo) TryAcquireConsumerLock(context.Context) (SchedulerOutboxConsumerLease, bool, error) {
	return &producerIsolationLease{repo: r}, true, nil
}
func (r *producerIsolationRepo) enqueueSameKey(attempted chan<- bool) {
	blocked := false
	select {
	case <-r.keyReleased:
	default:
		blocked = true
		attempted <- true
		<-r.keyReleased
	}
	accountID := int64(7)
	r.events = append(r.events, SchedulerOutboxEvent{ID: 2, EventType: SchedulerOutboxEventAccountChanged, AccountID: &accountID})
	r.rows = append(r.rows, 2)
	if !blocked {
		attempted <- false
	}
}

type producerIsolationLease struct {
	repo *producerIsolationRepo
}

func (l *producerIsolationLease) KeepAlive(context.Context) error { return nil }
func (l *producerIsolationLease) Release()                        { l.repo.releaseDedupKey() }

type producerIsolationAccountRepo struct{ outboxCleanupAccountRepo }

func (*producerIsolationAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return &Account{ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}, nil
}

type producerIsolationCache struct {
	outboxCleanupCache
	started chan struct{}
	resume  chan struct{}
}

func (c *producerIsolationCache) SetAccount(context.Context, *Account) error {
	close(c.started)
	<-c.resume
	return nil
}

func TestSchedulerOutboxSameKeyProducerDoesNotWaitForEventHandling(t *testing.T) {
	accountID := int64(7)
	repo := &producerIsolationRepo{outboxCleanupRepo: outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, EventType: SchedulerOutboxEventAccountChanged, AccountID: &accountID}}, rows: []int64{1},
	}, keyReleased: make(chan struct{})}
	cache := &producerIsolationCache{started: make(chan struct{}), resume: make(chan struct{})}
	svc := NewSchedulerSnapshotService(cache, repo, &producerIsolationAccountRepo{}, nil, nil)
	handled := make(chan struct{})
	go func() { svc.pollOutbox(); close(handled) }()
	<-cache.started // event projection is held, with the lease still owned
	attempted := make(chan bool)
	produced := make(chan struct{})
	go func() { repo.enqueueSameKey(attempted); close(produced) }()
	blocked := <-attempted // explicit outcome, not a timed guess about scheduling
	close(cache.resume)
	<-handled
	<-produced
	require.False(t, blocked, "a same-key producer must finish before event handling resumes")
	require.Equal(t, []int64{2}, repo.rows, "acknowledge only the original batch, never the new same-key event")
	require.Len(t, repo.events, 1)
	require.Equal(t, int64(2), repo.events[0].ID)
}
