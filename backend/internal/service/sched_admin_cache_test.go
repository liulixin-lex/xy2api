//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
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
	_, err := s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	require.False(t, groups.deadline.IsZero())
	require.WithinDuration(t, start.Add(10*time.Second), groups.deadline, time.Second)
	require.True(t, groups.deadline.Equal(queryDeadline), "pre-query work must not reset the request's 10 second budget")
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
