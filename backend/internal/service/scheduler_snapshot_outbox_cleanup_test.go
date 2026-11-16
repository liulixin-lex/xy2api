package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type outboxCleanupCache struct {
	watermark       int64
	setWatermarks   []int64
	updateErr       error
	listBucketErr   error
	listBuckets     []SchedulerBucket
	listBucketCalls int
}

func (c *outboxCleanupCache) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

func (c *outboxCleanupCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *outboxCleanupCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) error {
	return nil
}

func (c *outboxCleanupCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *outboxCleanupCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *outboxCleanupCache) TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	return SchedulerGroupLifecycleLease{}, false, nil
}

func (c *outboxCleanupCache) ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error {
	return nil
}

func (c *outboxCleanupCache) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	return nil, nil
}

func (c *outboxCleanupCache) SetAccount(ctx context.Context, account *Account) error {
	return nil
}

func (c *outboxCleanupCache) DeleteAccount(ctx context.Context, accountID int64) error {
	return nil
}

func (c *outboxCleanupCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return c.updateErr
}

func (c *outboxCleanupCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	return true, nil
}

func (c *outboxCleanupCache) UnlockBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *outboxCleanupCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.listBucketCalls++
	return c.listBuckets, c.listBucketErr
}

func (c *outboxCleanupCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return c.watermark, nil
}

func (c *outboxCleanupCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	c.watermark = id
	c.setWatermarks = append(c.setWatermarks, id)
	return nil
}

type outboxCleanupRepo struct {
	events          []SchedulerOutboxEvent
	rows            []int64
	countCalls      int
	countErr        error
	lockUnavailable bool
	lockAttempts    int
	releaseCount    int
	deleteCalls     [][]int64
	deleteErr       error
	pendingReads    int
}

type outboxCleanupAccountRepo struct {
	AccountRepository
}

func (r *outboxCleanupAccountRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}

type blockingOutboxCleanupCache struct {
	*outboxCleanupCache
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (c *blockingOutboxCleanupCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		close(c.started)
		<-c.release
	}
	return c.listBuckets, c.listBucketErr
}

func (c *blockingOutboxCleanupCache) listCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (r *outboxCleanupRepo) ListPendingAndReleaseDedup(_ context.Context, limit int) ([]SchedulerOutboxEvent, error) {
	events := append([]SchedulerOutboxEvent(nil), r.events...)
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
func (r *outboxCleanupRepo) OldestPendingCreatedAt(context.Context) (time.Time, bool, error) {
	r.pendingReads++
	if len(r.events) == 0 {
		return time.Time{}, false, nil
	}
	oldest := r.events[0].CreatedAt
	for _, e := range r.events {
		if e.CreatedAt.Before(oldest) {
			oldest = e.CreatedAt
		}
	}
	return oldest, true, nil
}
func (r *outboxCleanupRepo) PendingCount(context.Context) (int64, error) {
	r.countCalls++
	return int64(len(r.rows)), r.countErr
}
func (r *outboxCleanupRepo) DeleteProcessed(_ context.Context, ids []int64) error {
	r.deleteCalls = append(r.deleteCalls, append([]int64(nil), ids...))
	if r.deleteErr != nil {
		return r.deleteErr
	}
	consumed := make(map[int64]bool, len(ids))
	for _, id := range ids {
		consumed[id] = true
	}
	kept := r.rows[:0]
	for _, id := range r.rows {
		if !consumed[id] {
			kept = append(kept, id)
		}
	}
	r.rows = kept
	events := r.events[:0]
	for _, event := range r.events {
		if !consumed[event.ID] {
			events = append(events, event)
		}
	}
	r.events = events
	return nil
}
func (r *outboxCleanupRepo) TryAcquireConsumerLock(context.Context) (SchedulerOutboxConsumerLease, bool, error) {
	r.lockAttempts++
	if r.lockUnavailable {
		return nil, false, nil
	}
	r.lockUnavailable = true
	return outboxCleanupLease{release: func() { r.lockUnavailable = false; r.releaseCount++ }}, true, nil
}

type outboxCleanupLease struct {
	release func()
}

func (l outboxCleanupLease) Release() {
	if l.release != nil {
		l.release()
	}
}

func TestSchedulerSnapshotServicePollOutboxAcknowledgesOnlySelectedIDs(t *testing.T) {
	cache := &outboxCleanupCache{watermark: 10000}
	repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 3, EventType: SchedulerOutboxEventAccountLastUsed}}, rows: []int64{1, 2, 3, 4}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
	svc.pollOutbox()
	require.Equal(t, []int64{1, 2, 4}, repo.rows)
	require.Equal(t, [][]int64{{3}}, repo.deleteCalls)
	require.Empty(t, cache.setWatermarks, "Redis watermark is not a commit cursor")
	require.Equal(t, 1, repo.releaseCount)
}
func TestSchedulerSnapshotServicePollOutboxSkipsLockedConsumer(t *testing.T) {
	repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 3}}, rows: []int64{3}, lockUnavailable: true}
	svc := NewSchedulerSnapshotService(&outboxCleanupCache{}, repo, nil, nil, nil)
	svc.pollOutbox()
	require.Empty(t, repo.deleteCalls)
	require.Equal(t, []int64{3}, repo.rows)
	require.Zero(t, repo.releaseCount)
}
func TestSchedulerSnapshotServicePollOutboxRetainsBatchOnFailure(t *testing.T) {
	for _, stage := range []string{"handle", "ack"} {
		t.Run(stage, func(t *testing.T) {
			cache := &outboxCleanupCache{}
			repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 5, EventType: SchedulerOutboxEventAccountLastUsed, Payload: map[string]any{"last_used": map[string]any{"101": int64(123)}}}}, rows: []int64{5, 6}}
			if stage == "handle" {
				cache.updateErr = errors.New("cache failed")
			} else {
				repo.deleteErr = errors.New("ack failed")
			}
			svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
			svc.pollOutbox()
			require.Equal(t, []int64{5, 6}, repo.rows)
			require.Len(t, repo.events, 1)
			require.Equal(t, 1, repo.releaseCount)
			cache.updateErr, repo.deleteErr = nil, nil
			svc.pollOutbox()
			require.Equal(t, []int64{6}, repo.rows)
			require.Empty(t, repo.events)
		})
	}
}
func TestSchedulerSnapshotServicePollOutboxDoesNotUseConsumedEventForLag(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 7, EventType: SchedulerOutboxEventAccountLastUsed, CreatedAt: time.Now().Add(-time.Hour)}}}
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{OutboxLagWarnSeconds: 1, OutboxLagRebuildSeconds: 1, OutboxLagRebuildFailures: 1}}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)
	svc.pollOutbox()
	require.Equal(t, 1, repo.pendingReads)
	require.Zero(t, cache.listBucketCalls)
	require.Zero(t, svc.lagFailures)
}

func TestSchedulerSnapshotServiceCheckOutboxLagLatchesPersistentDegradation(t *testing.T) {
	tests := []struct {
		name             string
		createdAt        time.Time
		rows             []int64
		lagSeconds       int
		backlogThreshold int
	}{
		{
			name:       "lag",
			createdAt:  time.Now().Add(-time.Hour),
			rows:       []int64{1},
			lagSeconds: 1,
		},
		{
			name:             "backlog",
			createdAt:        time.Now(),
			rows:             int64Range(1, 100),
			backlogThreshold: 50,
		},
		{
			name:             "lag_and_backlog",
			createdAt:        time.Now().Add(-time.Hour),
			rows:             int64Range(1, 100),
			lagSeconds:       1,
			backlogThreshold: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &outboxCleanupCache{listBuckets: []SchedulerBucket{{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}}
			repo := &outboxCleanupRepo{
				events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: tt.createdAt}},
				rows:   tt.rows,
			}
			cfg := &config.Config{
				Gateway: config.GatewayConfig{
					Scheduling: config.GatewaySchedulingConfig{
						OutboxLagRebuildSeconds:  tt.lagSeconds,
						OutboxLagRebuildFailures: 1,
						OutboxBacklogRebuildRows: tt.backlogThreshold,
					},
				},
			}
			svc := NewSchedulerSnapshotService(cache, repo, &outboxCleanupAccountRepo{}, nil, cfg)

			for range 3 {
				svc.checkOutboxLag(context.Background())
			}

			if cache.listBucketCalls != 1 {
				t.Fatalf("expected one rebuild attempt during a persistent degraded episode, got %d", cache.listBucketCalls)
			}
		})
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRearmsAfterRecovery(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 1,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	svc.checkOutboxLag(context.Background())
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected a failed rebuild to stay bounded within the episode, got %d attempts", cache.listBucketCalls)
	}

	repo.events, repo.rows = nil, nil
	svc.checkOutboxLag(context.Background())
	repo.events = append(repo.events, SchedulerOutboxEvent{ID: 2, CreatedAt: time.Now().Add(-time.Hour)})
	repo.rows = []int64{2}
	svc.checkOutboxLag(context.Background())

	if cache.listBucketCalls != 2 {
		t.Fatalf("expected recovery to rearm a failed rebuild for the next episode, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRetriesAfterCooldownWithoutRecovery(t *testing.T) {
	cache := &outboxCleanupCache{
		listBucketErr: errors.New("list buckets failed"),
		listBuckets:   []SchedulerBucket{{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}},
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:    1,
				OutboxLagRebuildFailures:   1,
				FullRebuildIntervalSeconds: 0,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, &outboxCleanupAccountRepo{}, nil, cfg)

	svc.checkOutboxLag(context.Background())
	for range 3 {
		svc.checkOutboxLag(context.Background())
	}
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected failed rebuild polls to be rate limited, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	if !svc.outboxRebuildRetryAt.After(time.Now()) {
		t.Fatal("expected failed rebuild to schedule a future retry")
	}
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected persistent degradation to retry after cooldown, got %d attempts", cache.listBucketCalls)
	}

	for range 3 {
		svc.checkOutboxLag(context.Background())
	}
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected repeated rebuild failures to stay rate limited, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	cache.listBucketErr = nil
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 3 {
		t.Fatalf("expected degraded episode to retry after cooldown, got %d attempts", cache.listBucketCalls)
	}

	for range 3 {
		svc.checkOutboxLag(context.Background())
	}
	if cache.listBucketCalls != 3 {
		t.Fatalf("expected successful retry to latch the degraded episode, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetryDoesNotBypassNewLagThreshold(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   int64Range(1, 100),
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 3,
				OutboxBacklogRebuildRows: 50,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	// Start with backlog-only degradation and leave its failed rebuild retry due.
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the backlog degradation to attempt one rebuild, got %d", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()

	// The backlog recovers while lag becomes newly degraded. The stale backlog
	// retry must not make the first lag observation bypass its failure threshold.
	repo.rows = []int64{1}
	repo.events[0].CreatedAt = time.Now().Add(-time.Hour)
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the new lag episode to start at its own threshold, got %d rebuild attempts", cache.listBucketCalls)
	}

	svc.checkOutboxLag(context.Background())
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected lag rebuild only after three lag observations, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagLagRetryDoesNotDelayOrEscalateNewBacklog(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 1,
				OutboxBacklogRebuildRows: 50,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	// Start with lag-only degradation and a failed rebuild in cooldown.
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the lag degradation to attempt one rebuild, got %d", cache.listBucketCalls)
	}

	// Lag recovers while backlog becomes newly degraded. It must start immediately
	// and its first failure must use the base retry generation, not lag's count.
	repo.events[0].CreatedAt = time.Now()
	repo.rows = int64Range(1, 100)
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected the new backlog degradation not to inherit lag cooldown, got %d rebuild attempts", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	failures := svc.outboxRebuildFailures
	svc.lagMu.Unlock()
	if failures != 1 {
		t.Fatalf("expected backlog retry failures to restart at one, got %d", failures)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetrySurvivesUnknownBacklog(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   int64Range(1, 100),
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxBacklogRebuildRows: 50,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	// A failed backlog rebuild starts a reason-scoped cooldown.
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected one initial backlog rebuild, got %d", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	retryAt := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if !retryAt.After(time.Now()) {
		t.Fatalf("expected a future backlog retry, got %s", retryAt)
	}

	// A temporary MaxID failure makes backlog health unknown, not recovered.
	repo.countErr = errors.New("max id unavailable")
	svc.checkOutboxLag(context.Background())
	svc.lagMu.Lock()
	retryReason := svc.outboxRebuildRetryReason
	failures := svc.outboxRebuildFailures
	retryAtAfterUnknown := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if retryReason != "outbox_backlog" || failures != 1 || !retryAtAfterUnknown.Equal(retryAt) {
		t.Fatalf("expected unknown backlog to preserve retry state, got reason=%q failures=%d retry_at=%s", retryReason, failures, retryAtAfterUnknown)
	}

	// When MaxID recovers and backlog remains degraded, the original cooldown
	// still applies; only an expired cooldown may trigger the retry.
	repo.countErr = nil
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected backlog recovery before cooldown to stay rate limited, got %d attempts", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected backlog retry after cooldown expiry, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagPreemptsUnknownBacklogRetryAtThreshold(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   int64Range(1, 100),
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 3,
				OutboxBacklogRebuildRows: 50,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	// Backlog starts the first failed rebuild generation and remains unknown.
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected one initial backlog rebuild, got %d", cache.listBucketCalls)
	}
	repo.countErr = errors.New("max id unavailable")
	repo.events[0].CreatedAt = time.Now().Add(-time.Hour)

	// A known lag degradation must keep accumulating independently of the active
	// backlog cooldown and preempt it only after reaching its own threshold.
	for observation := 1; observation <= 2; observation++ {
		svc.checkOutboxLag(context.Background())
		if cache.listBucketCalls != 1 {
			t.Fatalf("expected lag observation %d to stay below threshold, got %d rebuild attempts", observation, cache.listBucketCalls)
		}
	}
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected lag to preempt backlog cooldown at its threshold, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	retryReason := svc.outboxRebuildRetryReason
	failures := svc.outboxRebuildFailures
	retryAt := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if retryReason != "outbox_lag" || failures != 1 || !retryAt.After(time.Now()) {
		t.Fatalf("expected a fresh lag retry generation, got reason=%q failures=%d retry_at=%s", retryReason, failures, retryAt)
	}
}

func TestOutboxRebuildRetryDelayIsExponentiallyBounded(t *testing.T) {
	previous := time.Duration(0)
	for failures := 1; failures <= 20; failures++ {
		delay := outboxRebuildRetryDelay(failures)
		if delay < previous {
			t.Fatalf("expected retry delay to be monotonic, failure %d produced %s after %s", failures, delay, previous)
		}
		if delay > outboxRebuildRetryMaxDelay {
			t.Fatalf("expected retry delay to stay bounded, got %s", delay)
		}
		previous = delay
	}
	if previous != outboxRebuildRetryMaxDelay {
		t.Fatalf("expected repeated failures to reach max delay %s, got %s", outboxRebuildRetryMaxDelay, previous)
	}
}

func TestSchedulerSnapshotServicePollOutboxEmptyBatchClearsDegradedEpisode(t *testing.T) {
	cache := &outboxCleanupCache{listBuckets: []SchedulerBucket{{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 1,
				OutboxBacklogRebuildRows: 1,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, &outboxCleanupAccountRepo{}, nil, cfg)

	svc.checkOutboxLag(context.Background())
	repo.events, repo.rows = nil, nil
	svc.pollOutbox()

	if repo.pendingReads != 1 {
		t.Fatalf("expected empty poll to use the empty batch as recovery evidence, got reads %#v", repo.pendingReads)
	}
	if repo.countCalls != 1 {
		t.Fatalf("expected empty poll to skip a redundant backlog query, got %d health checks", repo.countCalls)
	}

	repo.events = append(repo.events, SchedulerOutboxEvent{ID: 2, CreatedAt: time.Now().Add(-time.Hour)})
	repo.rows = []int64{2}
	svc.checkOutboxLag(context.Background())
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected empty-poll recovery to rearm the next degraded episode, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceOutboxLagWarningIsTransitionLimited(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)

	if !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected the initial degraded transition to log")
	}
	if svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected persistent degradation to suppress repeated warnings")
	}
	if svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected persistent degradation to suppress repeated warnings")
	}
	if svc.shouldLogOutboxLagWarning(false) {
		t.Fatal("expected recovery not to emit a lag warning")
	}
	if !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected renewed degradation to log after recovery")
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagSamplesCountErrors(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	now := time.Now()

	if !svc.shouldLogOutboxCountError(now) {
		t.Fatal("expected the first PendingCount error to log")
	}
	if svc.shouldLogOutboxCountError(now.Add(outboxCountErrorLogSampleInterval / 2)) {
		t.Fatal("expected PendingCount errors inside the sample interval to be suppressed")
	}
	if !svc.shouldLogOutboxCountError(now.Add(outboxCountErrorLogSampleInterval)) {
		t.Fatal("expected PendingCount error logging to rearm after the sample interval")
	}
}

func TestSchedulerSnapshotServicePollOutboxHealthyEmptyBatchSkipsLagHealthQueries(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 1,
				OutboxBacklogRebuildRows: 1,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	svc.pollOutbox()

	if repo.pendingReads != 0 {
		t.Fatalf("expected healthy empty poll to skip lag query, got reads %#v", repo.pendingReads)
	}
	if repo.countCalls != 0 {
		t.Fatalf("expected healthy empty poll to skip backlog query, got %d calls", repo.countCalls)
	}
}

func TestSchedulerSnapshotServiceEmptyPollDoesNotReleaseRunningRebuild(t *testing.T) {
	baseCache := &outboxCleanupCache{
		watermark:   1,
		listBuckets: []SchedulerBucket{{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}},
	}
	cache := &blockingOutboxCleanupCache{
		outboxCleanupCache: baseCache,
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 1,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, &outboxCleanupAccountRepo{}, nil, cfg)

	firstDone := make(chan struct{})
	go func() {
		svc.checkOutboxLag(context.Background())
		close(firstDone)
	}()
	select {
	case <-cache.started:
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not start")
	}

	// The empty batch proves recovery for episode/retry state, but it must not
	// release ownership of the still-running rebuild.
	repo.events, repo.rows = nil, nil
	svc.pollOutbox()

	secondDone := make(chan struct{})
	go func() {
		svc.checkOutboxLag(context.Background())
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
		close(cache.release)
		<-firstDone
		<-secondDone
		t.Fatal("second lag check queued another rebuild while the first was running")
	}

	close(cache.release)
	<-firstDone
	if calls := cache.listCalls(); calls != 1 {
		t.Fatalf("expected one rebuild generation, got %d", calls)
	}
}

func int64Range(start, end int64) []int64 {
	values := make([]int64, 0, end-start+1)
	for id := start; id <= end; id++ {
		values = append(values, id)
	}
	return values
}
