//go:build unit

package resilience

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type collabPanicLog struct {
	level   slog.Level
	message string
	fields  map[string]any
}
type collabPanicHandler chan collabPanicLog

func (h collabPanicHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h collabPanicHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "resilience.worker_panic" {
		return nil
	}
	fields := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { fields[a.Key] = a.Value.Any(); return true })
	h <- collabPanicLog{r.Level, r.Message, fields}
	return nil
}
func (h collabPanicHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h collabPanicHandler) WithGroup(string) slog.Handler      { return h }
func TestCollabWorkerPanicLogsStack(t *testing.T) {
	logs := make(collabPanicHandler, 1)
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })
	before := CollabSafetySnapshot().ConsumerPanics
	done := make(chan struct{})
	go func() { defer close(done); defer RecoverCollabWorker(); panic("synthetic worker failure") }()
	<-done
	require.Equal(t, before+1, CollabSafetySnapshot().ConsumerPanics)
	select {
	case event := <-logs:
		require.Equal(t, slog.LevelError, event.level)
		require.Equal(t, "synthetic worker failure", event.fields["panic"])
		require.True(t, strings.Contains(event.fields["stack"].(string), "TestCollabWorkerPanicLogsStack"))
	default:
		t.Fatal("worker panic was not logged")
	}
}
func TestCollabRetryAdviceWorkerChannel(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "panic"}[panics], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				wanted := RetryAdvice{Seconds: 7, Source: "recovery"}
				ctx := WithRetryAdviceProvider(context.Background(), func(context.Context) RetryAdvice {
					if panics {
						panic("synthetic advice failure")
					}
					return wanted
				})
				start := time.Now()
				got := ResolveRetryAdvice(ctx)
				if panics {
					require.Equal(t, RetryAdvice{}, got)
				} else {
					require.Equal(t, wanted, got)
				}
				require.Zero(t, time.Since(start), "a closed empty channel must yield unknown advice immediately")
			})
		})
	}
}
