//go:build unit

package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"testing"
	"time"
)

type collabHungOptionalBody struct{ ctx context.Context }

func (b *collabHungOptionalBody) Read([]byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-time.After(70 * time.Second):
		return 0, io.EOF
	}
}
func (*collabHungOptionalBody) Close() error { return nil }

func TestCollabRegressionPreemptedAccountWaiters(t *testing.T) {
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
	entered := false
	held2, held10 := 0, 0
	setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
		h.collabRescuable = collabCanRescue
		h.collabRescue = func(c *gin.Context, seam *collabSeam, saved *collabWithheld) {
			if seam.losslessDuration > 0 && !entered {
				entered = true
				rr := seam.observer.Rejections()
				t.Logf("first_rejected=%d last_rejected=%d", rr[0].AccountID, rr[len(rr)-1].AccountID)
				slots.mu.Lock()
				require.Equal(t, 1, slots.active[slots.base+2])
				slots.mu.Unlock()
				done = make(chan struct{})
				go func() {
					time.Sleep(100 * time.Millisecond)
					acquired, err := slots.AcquireAccountSlot(context.Background(), slots.base+2, 2, "synthetic-competitor")
					require.NoError(t, err)
					require.True(t, acquired)
					slots.mu.Lock()
					slots.secondWaiters = true
					slots.mu.Unlock()
					time.Sleep(2 * time.Second)
					slots.mu.Lock()
					held2 = slots.active[slots.base+2]
					slots.mu.Unlock()
					time.Sleep(8 * time.Second)
					slots.mu.Lock()
					held10 = slots.active[slots.base+2]
					slots.mu.Unlock()
					close(done)
				}()
			}
			h.rescueCollab(c, seam, saved)
		}
	})
	collabResponseFault = func(req *http.Request, resp *http.Response) {
		if resilience.CollabAttemptFrom(req.Context()).Optional && resp != nil {
			resp.Body = &collabHungOptionalBody{req.Context()}
		}
	}
	cfg := resilience.Defaults()
	cfg.Collab.Ladder = true
	cfg.Collab.LosslessFirst = true
	c := collabSafetyCase(t, "task_then_reasoning_elsewhere/pass_false/stream_true/attempts_4")
	c.Name = "regression_switched_before_preemption"
	c.Attempts = 0
	c.First = []replayRule{{"server_error", ""}}
	c.Second = []replayRule{{"named", "amsg_task"}}
	got := runCollaborationReplay(t, c, cfg).Turns[0]
	t.Logf("entered=%t held_second_2s=%d held_second_10s=%d terminal_ms=%d sends=%v", entered, held2, held10, got.TerminalMillis, got.SendsPerAccount)
	require.True(t, entered)
	require.LessOrEqual(t, held2, 1, "only competitor slot may remain after a waiter appears on the retained account")
	require.LessOrEqual(t, held10, 1)
}
