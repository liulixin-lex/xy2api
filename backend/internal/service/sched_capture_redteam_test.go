//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCaptureRedNativeBufferedUsageBeforeDisconnect(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name                  string
		input, read, creation int
	}{
		{"cache_read", 20, 80, 0},
		{"cache_creation", 20, 30, 50},
		{"all_cached", 0, 60, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var control schedquality.AttemptSample
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprintf("write_failed=%v", fail), func(t *testing.T) {
					c, _ := compatStreamContext("/v1/messages", "")
					if fail {
						c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer}
					}
					state := resilience.NewRequestState()
					state.EnableGatewayRequest(time.Now())
					state.NoteGatewaySend(11)
					state.SetSchedSelection(resilience.SchedSelection{AccountID: 11, Model: "fixture"})
					ctx := resilience.WithRequestState(c.Request.Context(), state)
					c.Request = c.Request.WithContext(ctx)
					recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
					t.Cleanup(recorder.Stop)
					var rows []schedquality.AttemptSample
					recorder.SetObserver(func(row schedquality.AttemptSample) {
						if !row.FinishedAt.IsZero() {
							rows = append(rows, row)
						}
					})
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), schedAttemptRecorder: recorder}
					account := &Account{ID: 11, Platform: PlatformOpenAI}
					a := svc.beginSchedAttempt(ctx, account, nil, 0, false)
					payload := fmt.Sprintf(`{"id":"fixture","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":2}}`, tc.input, tc.read, tc.creation)
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}
					result, err := svc.handleNativeAnthropicBufferedResponse(ctx, resp, c, account, "fixture", "fixture", "fixture", nil, time.Now())
					if fail {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
					require.NotNil(t, result)
					require.Equal(t, fail, result.ClientDisconnect)
					a.finish(result, err)
					require.Len(t, rows, 1)
					row := rows[0]
					require.Equal(t, captureInt(tc.input+tc.read+tc.creation), row.PromptTokens)
					require.Equal(t, captureInt(tc.input), row.InputTokens)
					require.Equal(t, captureInt(tc.read), row.CacheReadTokens)
					require.Equal(t, captureInt(tc.creation), row.CacheCreationTokens)
					require.Equal(t, *row.PromptTokens, result.Usage.InputTokens)
					require.Equal(t, *row.CacheReadTokens, result.Usage.CacheReadInputTokens)
					require.Equal(t, *row.CacheCreationTokens, result.Usage.CacheCreationInputTokens)
					if fail {
						require.Equal(t, schedquality.Outcome(5), row.Outcome)
						require.Equal(t, control.PromptTokens, row.PromptTokens)
						require.Equal(t, control.InputTokens, row.InputTokens)
						require.Equal(t, control.CacheReadTokens, row.CacheReadTokens)
						require.Equal(t, control.CacheCreationTokens, row.CacheCreationTokens)
					} else {
						require.Equal(t, schedquality.OutcomeOK, row.Outcome)
						control = row
					}
					state.CompleteGatewayForward(result.CompletedForDelivery(), result.ClientDisconnect)
					meta, _ := state.ClaimGatewayRequest(false)
					require.NotNil(t, meta)
					require.Equal(t, row.InputTokens, meta.InputTokens)
					require.Equal(t, row.CacheReadTokens, meta.CacheReadTokens)
					require.Equal(t, row.CacheCreationTokens, meta.CacheCreationTokens)
				})
			}
		})
	}
}

func TestCaptureRedRecorderFinishAfterStop(t *testing.T) {
	previous := slog.Default()
	logger.InitBootstrap()
	t.Cleanup(func() { slog.SetDefault(previous) })
	sink := NewOpsSystemLogSink(nil)
	t.Cleanup(sink.Stop)
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	store := &fakeSchedAttemptStore{}
	recorder := NewSchedAttemptRecorderService(store)
	attempt := recorder.Begin(SchedAttemptStart{AccountID: 11})
	recorder.Stop()
	finished := make(chan struct{})
	go func() {
		attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK})
		attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("Finish after Stop blocked")
	}
	require.Zero(t, recorder.Stats().QueueDepth)
	require.EqualValues(t, 1, recorder.Stats().DroppedWriteFailed)
	require.Zero(t, recorder.Stats().DroppedQueueFull)
	require.Empty(t, store.Rows)
	require.NoError(t, recorder.Flush(context.Background()), "stopped Flush keeps its existing result")
	require.Len(t, sink.queue, 1)
	event := <-sink.queue
	require.Equal(t, "sched_attempt.dropped", event.Message)
	require.Equal(t, "stopped", event.Fields["reason"])
	require.EqualValues(t, 1, event.Fields["dropped_write_failed"])
}

func TestCaptureRedRecorderConcurrentStopFinish(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		store := &fakeSchedAttemptStore{}
		recorder := NewSchedAttemptRecorderService(store)
		recorder.Start(context.Background())
		const count = 32
		attempts := make([]SchedAttempt, count)
		for i := range attempts {
			attempts[i] = recorder.Begin(SchedAttemptStart{AccountID: int64(i + 1)})
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(count + 1)
		for _, attempt := range attempts {
			go func() {
				defer wg.Done()
				<-start
				attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK})
			}()
		}
		go func() {
			defer wg.Done()
			<-start
			recorder.Stop()
		}()
		close(start)
		finished := make(chan struct{})
		go func() { wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Stop and Finish blocked")
		}
		stats := recorder.Stats()
		require.Zero(t, stats.QueueDepth)
		require.Zero(t, stats.DroppedQueueFull)
		require.EqualValues(t, count, uint64(len(store.Rows))+stats.DroppedWriteFailed, "every attempt is persisted or counted as dropped")
	}
}
