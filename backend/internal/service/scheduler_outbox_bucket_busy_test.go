//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Model real mutual exclusion and stable lifecycle epochs: taking a write
// token must not fence an older rebuild that already holds the bucket lock.
type eventBucketCache struct {
	outboxCleanupCache
	mu        sync.Mutex
	held      map[SchedulerBucket]bool
	snapshots map[SchedulerBucket][]Account
}

func (c *eventBucketCache) TryLockBucket(_ context.Context, b SchedulerBucket, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[b] {
		return false, nil
	}
	c.held[b] = true
	return true, nil
}
func (c *eventBucketCache) UnlockBucket(_ context.Context, b SchedulerBucket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.held, b)
	return nil
}
func (c *eventBucketCache) SetSnapshot(_ context.Context, b SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots[b] = append([]Account(nil), accounts...)
	return nil
}
func (c *eventBucketCache) members(b SchedulerBucket) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []int64
	for _, a := range c.snapshots[b] {
		ids = append(ids, a.ID)
	}
	return ids
}

type eventBucketAccounts struct {
	*batchAccountQueryRepo
	accounts map[int64]Account
}

func (r *eventBucketAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	a := r.accounts[id]
	return &a, nil
}

func TestSchedulerOutboxBusyBucketRetriesAfterOlderRebuild(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 50, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	old := Account{ID: 1, Platform: PlatformOpenAI, GroupIDs: []int64{50}, Status: StatusActive, Schedulable: true}
	joined := old
	joined.ID = 2
	other := old
	other.ID = 3
	other.GroupIDs = []int64{60}
	accounts := &eventBucketAccounts{batchAccountQueryRepo: newBatchAccountQueryRepo(), accounts: map[int64]Account{1: old, 2: joined, 3: other}}
	key := batchAccountQueryKey{groupID: 50, platform: PlatformOpenAI}
	accounts.results[key] = []batchAccountQueryResult{{accounts: []Account{old}}}
	for i := 0; i < 5; i++ {
		accounts.results[key] = append(accounts.results[key], batchAccountQueryResult{accounts: []Account{old, joined}})
	}
	accounts.results[batchAccountQueryKey{groupID: 60, platform: PlatformOpenAI}] = []batchAccountQueryResult{{accounts: []Account{other}}}
	cache := &eventBucketCache{held: make(map[SchedulerBucket]bool), snapshots: make(map[SchedulerBucket][]Account)}
	repo := &outboxCleanupRepo{}
	svc := NewSchedulerSnapshotService(cache, repo, accounts, nil, nil)
	started, resume := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	accounts.beforeRun = func(k batchAccountQueryKey) {
		if k == key && paused.CompareAndSwap(false, true) {
			close(started)
			<-resume
		}
	}
	oldDone := make(chan error, 1)
	go func() { oldDone <- svc.rebuildBuckets(context.Background(), []SchedulerBucket{bucket}, "interval") }()
	<-started // old membership has been read while this bucket is locked
	var resumeOnce sync.Once
	defer resumeOnce.Do(func() { close(resume) })
	repo.events = []SchedulerOutboxEvent{
		{ID: 1, EventType: SchedulerOutboxEventAccountGroupsChanged, AccountID: &joined.ID, CreatedAt: time.Now()},
		{ID: 2, EventType: SchedulerOutboxEventAccountChanged, AccountID: &old.ID, CreatedAt: time.Now()},
		{ID: 3, EventType: SchedulerOutboxEventAccountChanged, AccountID: &other.ID, CreatedAt: time.Now()},
	}
	repo.rows = []int64{1, 2, 3}
	svc.pollOutbox()
	require.Equal(t, []int64{1, 2}, repo.rows, "both events for the unfinished group must remain; seen must not hide the second")
	require.Equal(t, [][]int64{{3}}, repo.deleteCalls, "a later event for an independent bucket still completes")
	require.Equal(t, []int64{3}, cache.members(SchedulerBucket{GroupID: 60, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}))
	resumeOnce.Do(func() { close(resume) })
	require.NoError(t, <-oldDone)
	require.Equal(t, []int64{1}, cache.members(bucket), "older rebuild publishes its stale membership")
	svc.pollOutbox()
	require.Empty(t, repo.events)
	require.Equal(t, []int64{1, 2}, cache.members(bucket), "retained event repairs membership on the next poll")
	require.Equal(t, [][]int64{{3}, {1, 2}}, repo.deleteCalls)
}

func TestSchedulerOutboxFullRebuildRetainsBusyEvent(t *testing.T) {
	bucket := SchedulerBucket{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := &eventBucketCache{held: map[SchedulerBucket]bool{bucket: true}, snapshots: make(map[SchedulerBucket][]Account)}
	repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 1, EventType: SchedulerOutboxEventFullRebuild, CreatedAt: time.Now()}}, rows: []int64{1}}
	svc := NewSchedulerSnapshotService(cache, repo, newBatchAccountQueryRepo(), nil, &config.Config{RunMode: config.RunModeSimple})
	require.NoError(t, svc.triggerFullRebuild("interval"), "periodic rebuild may skip busy buckets")
	svc.pollOutbox()
	require.Equal(t, []int64{1}, repo.rows)
	require.NoError(t, cache.UnlockBucket(context.Background(), bucket))
	svc.pollOutbox()
	require.Empty(t, repo.rows)
	require.NotEmpty(t, cache.members(bucket))
}
