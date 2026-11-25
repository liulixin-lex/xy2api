//go:build unit

package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type perfTickLedger struct {
	perfReadStub
	now    func() time.Time
	delays []time.Duration
	loads  int
	at     time.Time
	ticks  []time.Time
}

func (l *perfTickLedger) PerfDatabaseTime(context.Context) (time.Time, error) {
	if l.loads < len(l.delays) {
		time.Sleep(l.delays[l.loads])
	}
	l.loads++
	return l.now(), nil
}

func (l *perfTickLedger) LoadPerfConfig(context.Context) (map[int64]SchedPolicyRecord, map[int64]time.Time, error) {
	l.ticks = append(l.ticks, l.at)
	return nil, nil, nil
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
					ledger.at = at
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
				t.Logf("wall=%s published=%s ticks=%v loads=%d", now().Format(time.RFC3339Nano), engine.PerformanceSnapshot().at.Format(time.RFC3339Nano), ledger.ticks, ledger.loads)
				require.Equal(t, want, engine.PerformanceSnapshot().at)
				require.Equal(t, []time.Time{tick, want}, ledger.ticks, "never repeat the old tick or replay missed ticks")
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
