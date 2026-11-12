//go:build unit

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpsSystemLogSinkSchedObsEventsPersistThroughSlog(t *testing.T) {
	previous := slog.Default()
	require.NoError(t, logger.Init(logger.InitOptions{
		Level: "info", Format: "json", ServiceName: "sched-observability-test", Environment: "test",
		Output: logger.OutputOptions{ToStdout: true},
	}))
	t.Cleanup(func() {
		logger.SetSink(nil)
		slog.SetDefault(previous)
	})
	var saved []*OpsInsertSystemLogInput
	sink := NewOpsSystemLogSink(&opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, rows []*OpsInsertSystemLogInput) (int64, error) {
		saved = append(saved, rows...)
		return int64(len(rows)), nil
	}})
	defer sink.Stop()
	logger.SetSink(sink)
	// Access log persistence is disabled; sched_quality info events still pass.
	sink.SetPersistAccessLogs(false)
	obs := NewSchedObservabilityService()
	obs.Count("tests", 3)
	obs.Count("tests_aborted", 1)
	obs.Count("cooled", 2)
	obs.Count("released", 4)
	obs.Cycle(123*time.Millisecond, true)
	obs.summary(context.Background())
	state := resilience.NewRequestState()
	f := selectionNewSessionFixture()
	result := f.selectNow(resilience.WithRequestState(context.Background(), state))
	require.NotNil(t, result)
	defer result.ReleaseFunc()

	require.Len(t, sink.queue, 2)
	batch := []*logger.LogEvent{<-sink.queue, <-sink.queue}
	for _, event := range batch {
		require.Equal(t, "slog", event.Component)
		require.Equal(t, "sched_quality", event.Fields["component"])
	}
	written, err := sink.flushBatch(context.Background(), batch)
	require.NoError(t, err)
	require.Equal(t, 2, written)
	require.Len(t, saved, 2)
	expected := []map[string]any{
		{"cycles": float64(1), "cycles_failed": float64(1), "cycle_ms_max": float64(123),
			"tests": float64(3), "tests_aborted": float64(1), "cooled": float64(2), "released": float64(4)},
		{"kind": "takeover", "gateway_request_id": state.GatewayRequestID(), "group_id": float64(7),
			"model": "m", "from_account_id": float64(10), "to_account_id": float64(1)},
	}
	for i, row := range saved {
		require.Equal(t, "info", row.Level)
		require.Equal(t, "sched_quality", row.Component)
		require.Equal(t, []string{"sched_quality.summary", "sched_quality.takeover"}[i], row.Message)
		require.False(t, row.CreatedAt.IsZero())
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(row.ExtraJSON), &fields))
		expected[i]["component"] = "sched_quality"
		expected[i]["service"] = "sched-observability-test"
		expected[i]["env"] = "test"
		require.Equal(t, expected[i], fields)
	}
	require.Equal(t, "m", saved[1].Model)
	require.Empty(t, saved[1].RequestID, "gateway_request_id remains in extra, not the HTTP request_id index")
}
