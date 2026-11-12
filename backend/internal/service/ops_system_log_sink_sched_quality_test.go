//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
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
