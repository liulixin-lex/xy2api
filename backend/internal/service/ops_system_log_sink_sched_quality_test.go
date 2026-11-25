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

func TestOpsSystemLogSinkSchedQualityInfoPersists(t *testing.T) {
	for _, fieldComponent := range []bool{false, true} {
		t.Run(map[bool]string{false: "component", true: "field"}[fieldComponent], func(t *testing.T) {
			var saved []*OpsInsertSystemLogInput
			sink := NewOpsSystemLogSink(&opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, rows []*OpsInsertSystemLogInput) (int64, error) {
				saved = append(saved, rows...)
				return int64(len(rows)), nil
			}})
			event := &logger.LogEvent{Level: "info", Component: "sched_quality", Message: "sched_quality.state"}
			if fieldComponent {
				event.Component = "app"
				event.Fields = map[string]any{"component": "sched_quality"}
			}
			sink.WriteLogEvent(event)
			sink.WriteLogEvent(&logger.LogEvent{Level: "info", Component: "app", Message: "unrelated"})
			require.Len(t, sink.queue, 1)
			written, err := sink.flushBatch(context.Background(), []*logger.LogEvent{<-sink.queue})
			require.NoError(t, err)
			require.Equal(t, 1, written)
			require.Len(t, saved, 1)
			require.Equal(t, "info", saved[0].Level)
			require.Equal(t, "sched_quality", saved[0].Component)
			require.Equal(t, "sched_quality.state", saved[0].Message)
		})
	}
}

func TestOpsSystemLogSinkNewSessionArraysPersist(t *testing.T) {
	previous := slog.Default()
	require.NoError(t, logger.Init(logger.InitOptions{
		Level: "info", Format: "json", ServiceName: "dispatch-test", Environment: "test",
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
	sink.SetPersistAccessLogs(false)
	good, unsupported := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
	unsupported.Credentials = map[string]any{"model_mapping": map[string]any{"other": "other"}}
	f := newSelectionRuntimeFixture(t, good, unsupported)
	state := resilience.NewRequestState()
	state.EnableGatewayRequest(time.Now())
	selected, err := selectOrdinaryNewSessionForTest(t, f, resilience.WithRequestState(context.Background(), state), "ordinary", nil, false)
	require.NoError(t, err)
	defer selected.ReleaseFunc()
	state.NoteGatewaySend(selected.Account.ID)
	state.CompleteGatewayForward(true, false)
	final, _ := state.ClaimGatewayRequest(true)
	require.Len(t, sink.queue, 1)
	written, err := sink.flushBatch(context.Background(), []*logger.LogEvent{<-sink.queue})
	require.NoError(t, err)
	require.Equal(t, 1, written)
	require.Len(t, saved, 1)
	require.Equal(t, "sched_quality.new_session", saved[0].Message)
	var extra map[string]any
	require.NoError(t, json.Unmarshal([]byte(saved[0].ExtraJSON), &extra))
	require.Equal(t, final.RequestID, extra["gateway_request_id"])
	require.Equal(t, resilience.RequestSuccess, final.Result)
	require.Equal(t, []newSessionCandidateLog{{AccountID: 1, Placement: "front", Rank: 1}}, requireNewSessionLists(t, extra, 1))
	require.Equal(t, []any{map[string]any{"account_id": float64(2), "reason": "model"}}, extra["excluded"])
}
