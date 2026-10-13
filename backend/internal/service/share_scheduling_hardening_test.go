//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func shareRecorderField(r *shareStatsRecorder, field string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for key, v := range r.counters {
		if key.Field == field {
			n += v
		}
	}
	return n
}

func TestShareSchedulingShadowComparisonsAreBounded(t *testing.T) {
	share := NewShareSchedulingService(nil, nil, nil)
	release := make(chan struct{})
	var started atomic.Int32
	run := func(int64) {
		started.Add(1)
		<-release
	}
	for range shareShadowConcurrency + 5 {
		share.startShadow(7, run, 1)
	}
	require.Eventually(t, func() bool { return started.Load() == shareShadowConcurrency }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, int64(5), shareRecorderField(share.stats, shareStatsShadowSkippedField),
		"comparisons beyond the bound are skipped and counted, not queued")

	close(release)
	require.Eventually(t, func() bool { return len(share.shadows) == 0 }, 5*time.Second, 5*time.Millisecond)
	share.startShadow(7, func(int64) { started.Add(1) }, 1)
	require.Eventually(t, func() bool { return started.Load() == shareShadowConcurrency+1 }, 5*time.Second, 5*time.Millisecond)
}

func TestShareSchedulingWaitSkipsCancelledRequests(t *testing.T) {
	conc := newShareTestConcurrency()
	conc.busy[1] = -1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec := &shareExecutor{ctx: ctx, concurrency: NewConcurrencyService(conc)}
	res, waited := exec.waitForSlot(&Account{ID: 1, Concurrency: 1}, 1, time.Second, 3)
	require.Nil(t, res)
	require.Zero(t, waited)
	require.Zero(t, conc.maxSeen[1], "a cancelled request never joins the wait queue")
	require.Zero(t, conc.waiting[1])
}

// shareCountingRepo counts usage aggregations and can fail them.
type shareCountingRepo struct {
	shareMemoryRepo
	mu    sync.Mutex
	calls int
	err   error
}

func (r *shareCountingRepo) AccountUsage(context.Context, int64, time.Time) (map[int64]ShareSchedulingUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return map[int64]ShareSchedulingUsage{1: {Requests: 90}, 2: {Requests: 10}}, nil
}

func (r *shareCountingRepo) state() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.err
}

func TestShareSchedulingCorrectionRefreshIsSpacedAndBounded(t *testing.T) {
	const gid = int64(9)
	policy := shareTestPolicy(sharesched.SchedulerShare,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 1},
		sharesched.AccountRule{AccountID: 2, Tier: 1, Weight: 1})
	policy.Balance = sharesched.Balance{Unit: sharesched.UnitRequests, Correction: sharesched.CorrectionGentle, WindowMinutes: 60}
	require.NoError(t, policy.Validate())
	repo := &shareCountingRepo{}
	share := NewShareSchedulingService(repo, nil, nil)
	setVersion := func(version int64) {
		share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true},
			groups: map[int64]*shareGroupPolicy{gid: {groupID: gid, version: version, policy: policy}}})
	}
	setVersion(1)
	ctx := context.Background()
	t0 := time.Now()
	calls := func() int { n, _ := repo.state(); return n }

	share.refreshCorrections(ctx, t0)
	require.Equal(t, 1, calls())
	factors := share.correctionsFor(gid)
	require.NotEmpty(t, factors)
	require.Equal(t, 5*time.Minute, shareCorrectionInterval(60), "a 60-minute window is re-read every 5 minutes")

	share.refreshCorrections(ctx, t0.Add(time.Minute))
	require.Equal(t, 1, calls(), "within the interval the window is not re-aggregated")
	require.Equal(t, factors, share.correctionsFor(gid))

	share.refreshCorrections(ctx, t0.Add(6*time.Minute))
	require.Equal(t, 2, calls())

	setVersion(2)
	share.refreshCorrections(ctx, t0.Add(7*time.Minute))
	require.Equal(t, 3, calls(), "a policy change recomputes at once")

	repo.mu.Lock()
	repo.err = errors.New("database unavailable")
	repo.mu.Unlock()
	share.refreshCorrections(ctx, t0.Add(13*time.Minute))
	require.Equal(t, 4, calls())
	require.Equal(t, factors, share.correctionsFor(gid), "recent factors survive a failed query")

	share.refreshCorrections(ctx, t0.Add(68*time.Minute))
	require.Equal(t, 5, calls())
	require.Empty(t, share.correctionsFor(gid), "factors older than the window are dropped, the group runs uncorrected")

	require.Equal(t, 30*time.Second, shareCorrectionInterval(5))
	require.Equal(t, 15*time.Minute, shareCorrectionInterval(1440))
}

// shareHangingRepo blocks every usage aggregation until its context ends.
type shareHangingRepo struct {
	shareMemoryRepo
	calls atomic.Int32
}

func (r *shareHangingRepo) AccountUsage(ctx context.Context, _ int64, _ time.Time) (map[int64]ShareSchedulingUsage, error) {
	r.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestShareSchedulingCorrectionRefreshEndsWithItsContext(t *testing.T) {
	policy := shareTestPolicy(sharesched.SchedulerShare,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 1},
		sharesched.AccountRule{AccountID: 2, Tier: 1, Weight: 1})
	policy.Balance = sharesched.Balance{Unit: sharesched.UnitRequests, Correction: sharesched.CorrectionGentle, WindowMinutes: 60}
	require.NoError(t, policy.Validate())
	groups := map[int64]*shareGroupPolicy{}
	for gid := int64(1); gid <= 5; gid++ {
		groups[gid] = &shareGroupPolicy{groupID: gid, version: 1, policy: policy}
	}
	repo := &shareHangingRepo{}
	share := NewShareSchedulingService(repo, nil, nil)
	share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true}, groups: groups})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	share.refreshCorrections(ctx, start)
	require.Less(t, time.Since(start), 2*time.Second, "a hanging database ends the pass with its context, not after a timeout per group")
	require.Equal(t, int32(5), repo.calls.Load())
	require.Empty(t, share.correctionsFor(1))
}

func TestShareSchedulingDecisionSamplesCappedPerFlush(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	for i := range maxShareDecisionsPerFlush + 20 {
		env.selectAccount(t, cacheableContext(), shareE2ESessions("cap", maxShareDecisionsPerFlush+20)[i], nil)
	}
	env.share.stats.mu.Lock()
	kept := len(env.share.stats.decisions[shareTestGroupID])
	dropped := env.share.stats.dropped
	env.share.stats.mu.Unlock()
	require.Equal(t, maxShareDecisionsPerFlush, kept)
	require.Equal(t, int64(20), dropped)
	require.Equal(t, int64(maxShareDecisionsPerFlush+20), env.counter(shareStatsRequestsField), "counters still see every decision")
}
