//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
