//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func captureSchedObsLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func schedObsLogRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		delete(record, "time")
		records = append(records, record)
	}
	return records
}

func schedObsTestClock(t *testing.T, s *SchedObservabilityService) (chan time.Time, *atomic.Bool) {
	t.Helper()
	ticks := make(chan time.Time, 1)
	stopped := &atomic.Bool{}
	s.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		require.Equal(t, 5*time.Minute, d)
		return ticks, func() { stopped.Store(true) }
	}
	return ticks, stopped
}

func TestSchedObsSummaryWindows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := captureSchedObsLogs(t)
		s := NewSchedObservabilityService()
		ticks, stopped := schedObsTestClock(t, s)
		s.Start(context.Background())
		defer s.Stop()
		s.Start(context.Background())
		s.Cycle(120*time.Millisecond, false)
		s.Cycle(5*time.Second, true)
		require.Empty(t, output.String(), "normal cycles do not log individually")

		ticks <- time.Now().Add(5 * time.Minute)
		synctest.Wait()
		require.Equal(t, []map[string]any{{
			"level": "INFO", "msg": "sched_quality.summary", "component": "sched_quality",
			"cycles": float64(2), "cycles_failed": float64(1), "cycle_ms_max": float64(5000),
			"cooled": float64(0), "released": float64(0),
		}}, schedObsLogRecords(t, output))

		output.Reset()
		ticks <- time.Now().Add(10 * time.Minute)
		synctest.Wait()
		require.Equal(t, []map[string]any{{
			"level": "INFO", "msg": "sched_quality.summary", "component": "sched_quality",
			"cycles": float64(0), "cycles_failed": float64(0), "cycle_ms_max": float64(0),
			"cooled": float64(0), "released": float64(0),
		}}, schedObsLogRecords(t, output))
		s.Count("cooled", 1)
		s.Stop()
		require.True(t, stopped.Load())
		require.Len(t, schedObsLogRecords(t, output), 1, "Stop does not flush a partial window")
	})
}

func TestSchedObsConcurrentCountersAndSlowCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := captureSchedObsLogs(t)
		s := NewSchedObservabilityService()
		ticks, _ := schedObsTestClock(t, s)
		s.Start(context.Background())
		defer s.Stop()
		s.Cycle(5001*time.Millisecond, false)
		var workers sync.WaitGroup
		for range 32 {
			workers.Go(func() {
				s.Count("cooled", 3)
				s.Count("released", 4)
				s.Cycle(6*time.Second, true)
			})
		}
		workers.Wait()
		require.Equal(t, []map[string]any{{
			"level": "WARN", "msg": "sched_quality.cycle", "component": "sched_quality", "duration_ms": float64(5001),
		}}, schedObsLogRecords(t, output))
		ticks <- time.Now().Add(5 * time.Minute)
		synctest.Wait()
		records := schedObsLogRecords(t, output)
		require.Len(t, records, 2)
		require.Equal(t, map[string]any{
			"level": "INFO", "msg": "sched_quality.summary", "component": "sched_quality",
			"cycles": float64(33), "cycles_failed": float64(32), "cycle_ms_max": float64(6000),
			"cooled": float64(96), "released": float64(128),
		}, records[1])

		output.Reset()
		s.Cycle(7*time.Second, false)
		s.Cycle(8*time.Second, false)
		ticks <- time.Now().Add(10 * time.Minute)
		synctest.Wait()
		records = schedObsLogRecords(t, output)
		require.Len(t, records, 2)
		require.Equal(t, map[string]any{
			"level": "WARN", "msg": "sched_quality.cycle", "component": "sched_quality", "duration_ms": float64(7000),
		}, records[0])
		require.Equal(t, map[string]any{
			"level": "INFO", "msg": "sched_quality.summary", "component": "sched_quality",
			"cycles": float64(2), "cycles_failed": float64(0), "cycle_ms_max": float64(8000),
			"cooled": float64(0), "released": float64(0),
		}, records[1])
	})
}

func TestSchedObsContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := captureSchedObsLogs(t)
		s := NewSchedObservabilityService()
		_, stopped := schedObsTestClock(t, s)
		ctx, cancel := context.WithCancel(context.Background())
		s.Start(ctx)
		defer s.Stop()
		s.Count("cooled", 1)
		cancel()
		synctest.Wait()
		require.True(t, stopped.Load())
		require.Empty(t, output.String())
	})
}

type schedObsBlockedLogHandler struct {
	slog.Handler
	entered chan struct{}
	resume  <-chan struct{}
}

func (h *schedObsBlockedLogHandler) Handle(ctx context.Context, record slog.Record) error {
	close(h.entered)
	<-h.resume
	return h.Handler.Handle(ctx, record)
}

func TestSchedObsBlockedLoggingDoesNotBlockCounters(t *testing.T) {
	for _, event := range []string{"summary", "cycle"} {
		t.Run(event, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				output := captureSchedObsLogs(t)
				base := slog.Default()
				s := NewSchedObservabilityService()
				s.Count("cooled", 1)
				resume := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(resume) })
				defer unblock()
				handler := &schedObsBlockedLogHandler{Handler: base.Handler(), entered: make(chan struct{}), resume: resume}
				slog.SetDefault(slog.New(handler))
				logged := make(chan struct{})
				go func() {
					if event == "summary" {
						s.summary(context.Background())
					} else {
						s.Cycle(6*time.Second, false)
					}
					close(logged)
				}()
				<-handler.entered
				counted := make(chan struct{})
				go func() {
					s.Count("cooled", 1)
					s.Cycle(time.Second, false)
					close(counted)
				}()
				synctest.Wait()
				select {
				case <-counted:
				default:
					t.Fatal("blocked log writer must not block Count or Cycle")
				}
				unblock()
				<-logged
				slog.SetDefault(base)
				s.summary(context.Background())
				records := schedObsLogRecords(t, output)
				require.Len(t, records, 2)
				require.Equal(t, "sched_quality."+event, records[0]["msg"])
				wantCooled, wantCycles := float64(1), float64(1)
				if event == "cycle" {
					wantCooled, wantCycles = 2, 2
				}
				require.Equal(t, wantCooled, records[1]["cooled"])
				require.Equal(t, wantCycles, records[1]["cycles"])
			})
		})
	}
}
