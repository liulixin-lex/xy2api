//go:build unit

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var collabReplaySlotsSetup func(*replaySlots)
var collabReplayFinished func()

func TestCollabRegressionOriginalAccountWaiters(t *testing.T) {
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
					slots.firstWaiters = true
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
	cfg := resilience.Defaults()
	cfg.Collab.Ladder = true
	cfg.Collab.LosslessFirst = true
	c := collabSafetyCase(t, "task_then_reasoning_elsewhere/pass_false/stream_true/attempts_4")
	c.Second = []replayRule{{"regression_long_hang", ""}}
	c.Name = "regression_later_account_waiters"
	c.Attempts = 0
	got := runCollaborationReplay(t, c, cfg).Turns[0]
	t.Logf("entered=%t sampled=%t slots_2s=%d slots_10s=%d terminal_ms=%d", entered, sampled, held, heldLater, got.TerminalMillis)
	require.True(t, entered)
	require.True(t, sampled)
	require.LessOrEqual(t, held, 1)
	require.LessOrEqual(t, heldLater, 1)
}
