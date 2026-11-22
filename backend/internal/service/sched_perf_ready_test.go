//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type perfReadyLedger struct {
	perfReadStub
	fail string
}

func (l *perfReadyLedger) LoadPerfConfig(ctx context.Context) (map[int64]SchedPolicyRecord, map[int64]time.Time, error) {
	if l.fail == "config" {
		return nil, nil, errors.New("config unavailable")
	}
	return l.perfReadStub.LoadPerfConfig(ctx)
}

func (l *perfReadyLedger) LoadPerfSwitches(ctx context.Context, from, at time.Time, id int64, limit int) ([]SchedPerfSwitch, error) {
	if l.fail == "switches" {
		return nil, errors.New("switches unavailable")
	}
	return l.perfReadStub.LoadPerfSwitches(ctx, from, at, id, limit)
}

func TestSchedPerfReadyOnlyAfterCompletePublication(t *testing.T) {
	for _, failure := range []string{"partial", "incremental", "switches", "config"} {
		t.Run(failure, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			fact := perfFact(1, 1)
			fact.ID = "sample"
			next := SchedAttemptCursor{At: perfAt.Add(-time.Second), AttemptID: fact.ID}
			ledger := &perfReadyLedger{fail: failure}
			ledger.page = func(_, _ time.Time, after SchedAttemptCursor, incremental bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
				if !incremental && after.At.IsZero() {
					return []SchedPerfFact{fact}, next, false, nil
				}
				if ledger.fail == "partial" || ledger.fail == "incremental" && incremental {
					return nil, after, false, errors.New("page unavailable")
				}
				return nil, next, true, nil
			}
			engine := NewSchedQualityEngineService(nil, ledger, nil, func() time.Time { return perfAt }, nil)
			store := schedPerfStore{facts: map[string]SchedPerfFact{}}
			require.Error(t, engine.publishPerformance(t.Context(), &store, perfAt))
			require.False(t, engine.PerformanceSnapshot().complete)
			require.Empty(t, logs.String(), "failed or partial loads must not announce readiness")
			ledger.fail = ""
			require.NoError(t, engine.publishPerformance(t.Context(), &store, perfAt))
			require.True(t, engine.PerformanceSnapshot().Usable(perfAt))
			require.NoError(t, engine.publishPerformance(t.Context(), &store, perfAt))
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			require.Len(t, lines, 1, "refresh must not repeat the startup event")
			var event map[string]any
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
			require.Equal(t, "sched_quality.performance_ready", event["msg"])
			require.Equal(t, perfAt.Format(time.RFC3339Nano), event["snapshot_at"])
			require.Equal(t, perfAt.Add(-schedPerfWindow).Format(time.RFC3339Nano), event["from"])
			require.Equal(t, float64(1), event["rows"])
			require.GreaterOrEqual(t, event["load_ms"].(float64), float64(0))
			// Restart creates a fresh worker store and must announce its own first publication.
			restarted := schedPerfStore{facts: map[string]SchedPerfFact{}}
			require.NoError(t, engine.publishPerformance(t.Context(), &restarted, perfAt))
			require.Equal(t, 2, strings.Count(logs.String(), "sched_quality.performance_ready"))
		})
	}
}
