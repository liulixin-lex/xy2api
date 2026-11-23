//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabRegressionRetainedAccountAfterSemanticDelivery(t *testing.T) {
	for _, outcome := range []string{"waiter", "complete", "failure"} {
		t.Run(outcome, func(t *testing.T) { collabRetainedAfterSemantic(t, outcome) })
	}
}
func collabRetainedAfterSemantic(t *testing.T, outcome string) {
	collabSafetyReplay(t)
	var slots *replaySlots
	collabReplaySlotsSetup = func(s *replaySlots) { slots = s }
	var done chan struct{}
	collabReplayFinished = func() {
		if done != nil {
			<-done
		}
	}
	t.Cleanup(func() { collabReplaySlotsSetup = nil; collabReplayFinished = nil })
	entered, sampled := false, false
	held, heldLater := 0, 0
	setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
		h.collabRescuable = collabCanRescue
		h.collabRescue = func(c *gin.Context, seam *collabSeam, saved *collabWithheld) {
			if seam.losslessDuration > 0 && !entered {
				entered = true
				done = make(chan struct{})
				slots.mu.Lock()
				require.Equal(t, 1, slots.active[slots.base+1])
				slots.mu.Unlock()
				// The account fills and gains a waiter after optional admission.
				go func() {
					time.Sleep(100 * time.Millisecond)
					acquired, err := slots.AcquireAccountSlot(context.Background(), slots.base+1, 2, "synthetic-competitor")
					require.NoError(t, err)
					require.True(t, acquired)
					slots.mu.Lock()
					slots.firstWaiters = outcome == "waiter"
					slots.mu.Unlock()
					time.Sleep(2 * time.Second)
					slots.mu.Lock()
					held = slots.active[slots.base+1]
					slots.mu.Unlock()
					time.Sleep(8 * time.Second)
					slots.mu.Lock()
					heldLater = slots.active[slots.base+1]
					slots.mu.Unlock()
					sampled = true
					close(done)
				}()
			}
			h.rescueCollab(c, seam, saved)
		}
	})
	collabResponseFault = func(req *http.Request, resp *http.Response) {
		if !resilience.CollabAttemptFrom(req.Context()).Optional || resp == nil || resp.StatusCode != 200 {
			return
		}
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		split := strings.Index(string(raw), "event: response.completed")
		require.Greater(t, split, 0)
		if outcome == "failure" {
			raw = []byte(string(raw[:split]) + replayFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"code":"server_error","message":"synthetic failure"}}}`))
		}
		resp.Body = &collabDelayedCompletion{ctx: req.Context(), prefix: strings.NewReader(string(raw[:split])), tail: strings.NewReader(string(raw[split:]))}
	}
	cfg := resilience.Defaults()
	cfg.Collab.Ladder = true
	cfg.Collab.LosslessFirst = true
	c := collabSafetyCase(t, "task_then_reasoning_elsewhere/pass_false/stream_true/attempts_4")
	c.Second = nil
	c.Name = "regression_later_account_waiters"
	c.Attempts = 0
	got := runCollaborationReplay(t, c, cfg).Turns[0]
	t.Logf("success=%t accepted_ms=%d usage=%v entered=%t sampled=%t slots_2s=%d slots_10s=%d terminal_ms=%d", got.Success, got.AcceptedMillis, got.UsageAccounts, entered, sampled, held, heldLater, got.TerminalMillis)
	require.True(t, entered)
	require.True(t, sampled)
	require.LessOrEqual(t, held, 1)
	require.LessOrEqual(t, heldLater, 1)
}

type collabDelayedCompletion struct {
	ctx          context.Context
	prefix, tail *strings.Reader
	waited       bool
}

func (b *collabDelayedCompletion) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	if !b.waited {
		b.waited = true
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-time.After(20 * time.Second):
		}
	}
	return b.tail.Read(p)
}
func (*collabDelayedCompletion) Close() error { return nil }

func TestCollabRetainedSlotYieldsWhileOwnerRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var released atomic.Int64
		seam := &collabSeam{retained: &collabRetainedSlot{release: func() { released.Add(1) }}}
		start := time.Now()
		check := seam.watchRetainedWaiters(func(context.Context) bool { return time.Since(start) >= 100*time.Millisecond })
		ctx, stop := watchCollabTakeover(context.Background(), time.Minute, func() bool { return true }, check, check)
		defer stop()
		ownerDone := false
		go func() { time.Sleep(20 * time.Second); seam.retained.Release(); ownerDone = true }()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.Error(t, ctx.Err())
		require.False(t, ownerDone)
		require.EqualValues(t, 1, released.Load(), "the reservation must yield while its former owner is still running")
		time.Sleep(20 * time.Second)
		synctest.Wait()
		require.True(t, ownerDone)
		require.EqualValues(t, 1, released.Load(), "owner cleanup must not release twice")
	})
}
