//go:build unit

package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type perfTickLedger struct {
	perfReadStub
	now    func() time.Time
	delays []time.Duration
	mu     sync.Mutex
	loads  int
	at     time.Time
	ticks  []time.Time
	calls  []time.Time
	fail   bool
}

func (l *perfTickLedger) PerfDatabaseTime(ctx context.Context) (time.Time, error) {
	at := l.now()
	l.mu.Lock()
	l.calls = append(l.calls, at)
	wait := l.loads < len(l.delays)
	var delay time.Duration
	if wait {
		delay = l.delays[l.loads]
	}
	l.mu.Unlock()
	if wait {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-timer.C:
		}
	}
	l.mu.Lock()
	l.loads++
	fail := l.fail
	l.mu.Unlock()
	if fail {
		return time.Time{}, errors.New("fixture ledger unavailable")
	}
	return l.now(), nil
}

func (l *perfTickLedger) LoadPerfConfig(context.Context) (map[int64]SchedPolicyRecord, map[int64]time.Time, error) {
	l.mu.Lock()
	l.ticks = append(l.ticks, l.at)
	l.mu.Unlock()
	return nil, nil, nil
}

func (l *perfTickLedger) setAt(at time.Time) {
	l.mu.Lock()
	l.at = at
	l.mu.Unlock()
}

func (l *perfTickLedger) setFailure(fail bool) {
	l.mu.Lock()
	l.fail = fail
	l.mu.Unlock()
}

type perfTickLedgerSnapshot struct {
	calls, ticks []time.Time
	loads        int
}

func (l *perfTickLedger) snapshot() perfTickLedgerSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return perfTickLedgerSnapshot{calls: slices.Clone(l.calls), ticks: slices.Clone(l.ticks), loads: l.loads}
}

func TestSchedPerfTickPublication(t *testing.T) {
	for _, tt := range []struct {
		name       string
		clockShift time.Duration
		delays     []time.Duration
		elapsed    time.Duration
		nextTick   time.Duration
	}{
		{"early_wake", -time.Millisecond, []time.Duration{2 * time.Millisecond, 2 * time.Millisecond}, 181*time.Second + 4*time.Millisecond, schedPerfTick},
		{"early_wake_nanosecond", -time.Nanosecond, []time.Duration{2 * time.Nanosecond, 2 * time.Nanosecond}, 181*time.Second + 4*time.Nanosecond, schedPerfTick},
		{"early_wake_second", -time.Second, []time.Duration{2 * time.Second, 2 * time.Second}, 184 * time.Second, schedPerfTick},
		{"exact_boundary", 0, nil, 181 * time.Second, schedPerfTick},
		{"short_publication", 0, []time.Duration{2 * time.Second, 2 * time.Second}, 184 * time.Second, schedPerfTick},
		{"slow_start_one_period", 0, []time.Duration{6 * time.Minute}, 6*time.Minute + time.Millisecond, schedPerfTick},
		{"slow_start_multiple_periods", 0, []time.Duration{16 * time.Minute}, 16*time.Minute + time.Millisecond, 3 * schedPerfTick},
		{"late_wake", time.Second, nil, 181*time.Second + time.Millisecond, schedPerfTick},
		{"late_wake_multiple_periods", 6 * time.Minute, nil, 181*time.Second + time.Millisecond, 2 * schedPerfTick},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := time.Now()
				tick := perfAt.Add(130 * time.Minute)
				start := tick.Add(119 * time.Second)
				now := func() time.Time {
					elapsed := time.Since(base)
					// The wall clock shifts after the first deadline was armed.
					if elapsed >= 181*time.Second {
						elapsed += tt.clockShift
					}
					return start.Add(elapsed)
				}
				ledger := &perfTickLedger{now: now, delays: tt.delays}
				ledger.page = func(_, at time.Time, c SchedAttemptCursor, _ bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
					ledger.setAt(at)
					return nil, c, true, nil
				}
				engine := NewSchedQualityEngineService(nil, ledger, nil, now, nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan struct{})
				go func() { defer close(done); engine.runPerformance(ctx) }()
				synctest.Wait()
				time.Sleep(tt.elapsed)
				synctest.Wait()
				want := tick.Add(tt.nextTick)
				t.Logf("wall=%s published=%s ticks=%v loads=%d", now().Format(time.RFC3339Nano), engine.PerformanceSnapshot().at.Format(time.RFC3339Nano), ledger.snapshot().ticks, ledger.snapshot().loads)
				require.Equal(t, want, engine.PerformanceSnapshot().at)
				require.Equal(t, []time.Time{tick, want}, ledger.snapshot().ticks, "never repeat the old tick or replay missed ticks")
				cancel()
				<-done
				require.Nil(t, engine.PerformanceSnapshot(), "cancellation clears the published snapshot")
			})
		})
	}
}

func TestSchedPerfTickCancelDuringEarlyWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		now := func() time.Time {
			elapsed := time.Since(base)
			if elapsed >= 181*time.Second {
				elapsed -= time.Second
			}
			return perfAt.Add(119*time.Second + elapsed)
		}
		ledger := perfReadStub{page: func(_, _ time.Time, c SchedAttemptCursor, _ bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
			return nil, c, true, nil
		}}
		engine := NewSchedQualityEngineService(nil, ledger, nil, now, nil)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); engine.runPerformance(ctx) }()
		synctest.Wait()
		time.Sleep(181 * time.Second)
		synctest.Wait()
		cancel()
		<-done
		require.Nil(t, engine.PerformanceSnapshot())
	})
}

func TestSchedPerfTickFailureWaitsForNextBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		now := func() time.Time { return perfAt.Add(119*time.Second + time.Since(base)) }
		ledger := &perfTickLedger{now: now}
		ledger.setFailure(true)
		ledger.page = func(_, at time.Time, c SchedAttemptCursor, _ bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
			ledger.setAt(at)
			return nil, c, true, nil
		}
		engine := NewSchedQualityEngineService(nil, ledger, nil, now, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); engine.runPerformance(ctx) }()
		synctest.Wait()
		time.Sleep(181*time.Second - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, []time.Time{perfAt.Add(119 * time.Second)}, ledger.snapshot().calls)
		require.Empty(t, ledger.snapshot().ticks)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Len(t, ledger.snapshot().calls, 2)
		require.False(t, engine.PerformanceSnapshot().complete)
		ledger.setFailure(false)
		time.Sleep(schedPerfTick - time.Nanosecond)
		synctest.Wait()
		require.Len(t, ledger.snapshot().calls, 2, "quick failure cannot retry within its tick")
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, []time.Time{perfAt.Add(119 * time.Second), perfAt.Add(schedPerfTick), perfAt.Add(2 * schedPerfTick)}, ledger.snapshot().calls)
		require.Equal(t, []time.Time{perfAt.Add(2 * schedPerfTick)}, ledger.snapshot().ticks)
		require.Equal(t, perfAt.Add(2*schedPerfTick), engine.PerformanceSnapshot().at)
		require.True(t, engine.PerformanceSnapshot().complete)
		t.Logf("attempts=%v published=%v", ledger.snapshot().calls, ledger.snapshot().ticks)
		cancel()
		<-done
		require.Nil(t, engine.PerformanceSnapshot())
	})
}

func TestSchedPerfTickSustainedSlowLoads(t *testing.T) {
	for _, tt := range []struct {
		delay     time.Duration
		published []time.Duration
	}{
		{301 * time.Second, []time.Duration{0, schedPerfTick, 2 * schedPerfTick}},
		{16 * time.Minute, []time.Duration{0, 3 * schedPerfTick, 6 * schedPerfTick}},
	} {
		t.Run(tt.delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := time.Now()
				now := func() time.Time { return perfAt.Add(119*time.Second + time.Since(base)) }
				ledger := &perfTickLedger{now: now, delays: []time.Duration{tt.delay, tt.delay, tt.delay, tt.delay}}
				ledger.page = func(_, at time.Time, c SchedAttemptCursor, _ bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
					ledger.setAt(at)
					return nil, c, true, nil
				}
				engine := NewSchedQualityEngineService(nil, ledger, nil, now, nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan struct{})
				go func() { defer close(done); engine.runPerformance(ctx) }()
				synctest.Wait()
				time.Sleep(3 * tt.delay)
				synctest.Wait()
				var want []time.Time
				for _, d := range tt.published {
					want = append(want, perfAt.Add(d))
				}
				require.Equal(t, want, ledger.snapshot().ticks)
				require.Equal(t, want[len(want)-1], engine.PerformanceSnapshot().at)
				require.Equal(t, []time.Time{perfAt.Add(119 * time.Second), perfAt.Add(119*time.Second + tt.delay), perfAt.Add(119*time.Second + 2*tt.delay), now()}, ledger.snapshot().calls, "each load starts only when the previous load finishes")
				t.Logf("delay=%s attempts=%v published=%v", tt.delay, ledger.snapshot().calls, ledger.snapshot().ticks)
				cancel()
				<-done
				require.Equal(t, 3*tt.delay, time.Since(base), "cancel interrupts the fourth slow load")
				require.Nil(t, engine.PerformanceSnapshot())
			})
		})
	}
}
