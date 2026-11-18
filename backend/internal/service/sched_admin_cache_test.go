//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type schedAdminCacheEngine struct {
	SchedQualityEngine
	snapshot *SchedQualitySnapshot
}

func (e *schedAdminCacheEngine) Snapshot() *SchedQualitySnapshot { return e.snapshot }

type schedAdminConcurrentStats struct {
	SchedAdminAccountRepository
	load func(context.Context, []int64, time.Time) (map[int64]SchedAdminGroupLast60, error)
}

func (r *schedAdminConcurrentStats) SchedAdminRearEntries24h(context.Context, []int64, time.Time) (map[int64]SchedAdminRearEntries, error) {
	return nil, nil
}
func (r *schedAdminConcurrentStats) SchedAdminGroupLast60(ctx context.Context, ids []int64, at time.Time) (map[int64]SchedAdminGroupLast60, error) {
	return r.load(ctx, ids, at)
}
func schedAdminCacheFixture(repo SchedAdminAccountRepository) *SchedAdminService {
	now := time.Now()
	return NewSchedAdminService(SchedAdminDependencies{Engine: &schedAdminCacheEngine{snapshot: &SchedQualitySnapshot{At: now}}, Accounts: repo, Groups: schedAdminGroupMembers{schedAdminGroups{1: {ID: 1}, 2: {ID: 2}, 3: {ID: 3}}, []int64{}}, Now: time.Now})
}

func TestSchedAdminStatsSlowGroupDoesNotBlockCachedOrOtherGroups(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var mu sync.Mutex
	calls := map[int64]int{}
	repo := &schedAdminConcurrentStats{load: func(ctx context.Context, ids []int64, _ time.Time) (map[int64]SchedAdminGroupLast60, error) {
		require.Len(t, ids, 1)
		mu.Lock()
		calls[ids[0]]++
		mu.Unlock()
		if ids[0] == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		count := ids[0]
		return map[int64]SchedAdminGroupLast60{ids[0]: {Requests: &count}}, nil
	}}
	s := schedAdminCacheFixture(repo)
	cachedCount := int64(42)
	s.stats[2] = schedAdminStatsEntry{at: time.Now(), value: SchedAdminGroupLast60{Requests: &cachedCount}}
	first := make(chan error, 1)
	go func() { _, err := s.GroupsSummary(context.Background(), []int64{1}); first <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first query did not start")
	}
	// A hit and a different miss must complete while group 1's database call is still blocked.
	for _, id := range []int64{2, 3} {
		done := make(chan SchedAdminGroupsSummary, 1)
		errs := make(chan error, 1)
		go func() { out, err := s.GroupsSummary(context.Background(), []int64{id}); done <- out; errs <- err }()
		select {
		case out := <-done:
			require.NoError(t, <-errs)
			if id == 2 {
				require.EqualValues(t, 42, *out.Groups[0].Last60.Requests)
			}
		case <-time.After(time.Second):
			t.Fatalf("group %d waited behind group 1", id)
		}
	}
	// A request waiting on the same group can cancel independently of the shared query.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := s.GroupsSummary(ctx, []int64{1})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	mu.Lock()
	require.Equal(t, map[int64]int{1: 1, 3: 1}, calls)
	mu.Unlock()
	const waiters = 8
	joined := make(chan error, waiters)
	for range waiters {
		go func() {
			out, err := s.GroupsSummary(context.Background(), []int64{1})
			if err == nil && len(out.Groups) == 1 && out.Groups[0].Last60.Requests != nil && *out.Groups[0].Last60.Requests == 1 {
				joined <- nil
			} else {
				joined <- errors.New("shared group result missing")
			}
		}()
	}
	once.Do(func() { close(release) })
	require.NoError(t, <-first)
	for range waiters {
		select {
		case err := <-joined:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("shared waiter did not finish")
		}
	}
	mu.Lock()
	require.Equal(t, map[int64]int{1: 1, 3: 1}, calls)
	mu.Unlock()
}

func TestSchedAdminStatsInitiatorCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	repo := &schedAdminConcurrentStats{load: func(ctx context.Context, ids []int64, _ time.Time) (map[int64]SchedAdminGroupLast60, error) {
		close(started)
		select {
		case <-release:
			return map[int64]SchedAdminGroupLast60{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	s := schedAdminCacheFixture(repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := s.GroupsSummary(ctx, []int64{1}); first <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	cancel()
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("initiator did not cancel")
	}
	next := make(chan error, 1)
	go func() { _, err := s.GroupsSummary(context.Background(), []int64{1}); next <- err }()
	once.Do(func() { close(release) })
	select {
	case err := <-next:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("another waiter did not receive the result")
	}
}

func TestSchedAdminStatsInitiatorDeadlineDoesNotCancelOtherWaiters(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	count := int64(42)
	repo := &schedAdminConcurrentStats{load: func(ctx context.Context, ids []int64, _ time.Time) (map[int64]SchedAdminGroupLast60, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return map[int64]SchedAdminGroupLast60{ids[0]: {Requests: &count}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	s := schedAdminCacheFixture(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := s.groupLast60(ctx, []int64{1}, time.Now()); first <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	followerCtx, cancelFollower := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelFollower()
	type result struct {
		values map[int64]SchedAdminGroupLast60
		err    error
	}
	follower := make(chan result, 1)
	go func() {
		values, err := s.groupLast60(followerCtx, []int64{1}, time.Now())
		follower <- result{values, err}
	}()
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("initiator did not time out")
	}
	once.Do(func() { close(release) })
	select {
	case out := <-follower:
		require.NoError(t, followerCtx.Err())
		require.NoError(t, out.err)
		require.Equal(t, &count, out.values[1].Requests)
	case <-time.After(time.Second):
		t.Fatal("live waiter did not receive the shared result")
	}
	cached, err := s.groupLast60(context.Background(), []int64{1}, time.Now())
	require.NoError(t, err)
	require.Equal(t, &count, cached[1].Requests)
	require.EqualValues(t, 1, calls.Load(), "the result must be shared and cached after the initiator expires")
}

func TestSchedAdminStatsFreshCacheWithOlderRequestWindow(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	// This request captures its window before the first query publishes its cache.
	olderWindow := base.Add(time.Second)
	var calls atomic.Int32
	var queryWindow time.Time
	count := int64(42)
	repo := &schedAdminConcurrentStats{load: func(_ context.Context, ids []int64, at time.Time) (map[int64]SchedAdminGroupLast60, error) {
		calls.Add(1)
		queryWindow = at
		return map[int64]SchedAdminGroupLast60{ids[0]: {Requests: &count}}, nil
	}}
	s := schedAdminCacheFixture(repo)
	s.d.Now = func() time.Time { return base.Add(2 * time.Second) }
	_, err := s.groupLast60(context.Background(), []int64{1}, base)
	require.NoError(t, err)
	// Its delayed cache lookup must use the current clock, not olderWindow.
	out, err := s.groupLast60(context.Background(), []int64{1}, olderWindow)
	require.NoError(t, err)
	require.Equal(t, &count, out[1].Requests)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, base, queryWindow, "the statistics query must retain the original window")
}

type schedAdminBudgetGroups struct {
	schedAdminGroupMembers
	deadline time.Time
}

func (g *schedAdminBudgetGroups) GetByID(ctx context.Context, id int64) (*Group, error) {
	g.deadline, _ = ctx.Deadline()
	return g.schedAdminGroupMembers.GetByID(ctx, id)
}
func TestSchedAdminStatsBudgetStartsBeforeGroupLookup(t *testing.T) {
	var queryDeadline time.Time
	repo := &schedAdminConcurrentStats{load: func(ctx context.Context, _ []int64, _ time.Time) (map[int64]SchedAdminGroupLast60, error) {
		queryDeadline, _ = ctx.Deadline()
		return nil, nil
	}}
	s := schedAdminCacheFixture(repo)
	groups := &schedAdminBudgetGroups{schedAdminGroupMembers: s.d.Groups.(schedAdminGroupMembers)}
	s.d.Groups = groups
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.GroupsSummary(ctx, []int64{1})
	require.NoError(t, err)
	requestDeadline, _ := ctx.Deadline()
	require.Equal(t, requestDeadline, groups.deadline, "pre-query work must share the caller's budget")
	require.WithinDuration(t, start.Add(10*time.Second), queryDeadline, time.Second)
	require.True(t, queryDeadline.After(requestDeadline), "a shared query must have its own 10 second lifetime")
}
func TestSchedAdminStatsFailureIsNotCached(t *testing.T) {
	calls := 0
	failed := errors.New("statistics unavailable")
	repo := &schedAdminConcurrentStats{load: func(context.Context, []int64, time.Time) (map[int64]SchedAdminGroupLast60, error) {
		calls++
		if calls == 1 {
			return nil, failed
		}
		return nil, nil
	}}
	s := schedAdminCacheFixture(repo)
	_, err := s.GroupsSummary(context.Background(), []int64{1})
	require.ErrorIs(t, err, failed)
	_, err = s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	_, err = s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}
