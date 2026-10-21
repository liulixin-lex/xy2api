//go:build unit

package service

import (
	"context"
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
