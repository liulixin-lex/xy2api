//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestBudgetTraceRealTimersKeepFrozenOwners(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"header", "first_event", "gemini", "response_stage", "disarmed"} {
		t.Run(kind, func(t *testing.T) {
			ctx, trace := resilience.WithBudgetTraceForTest(context.Background())
			st := resilience.NewRequestState()
			ctx = resilience.WithRequestState(ctx, st)
			start := time.Now()
			st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start.Add(-240*time.Second+80*time.Millisecond)))
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, Model: "original", AttemptStart: start, Warm: true, Stream: true})
			plan := st.PlanAttemptTimeout(time.Second, false, start)
			var done <-chan struct{}
			switch kind {
			case "header":
				guarded, g := newOpenAIFirstOutputHeaderGuard(ctx, func() {}, start.Add(plan.Effective()), plan)
				defer g.close()
				done = guarded.Done()
			case "first_event", "disarmed":
				guarded, g := newFirstEventGuard(ctx, plan.Effective(), plan)
				defer g.Release()
				done = guarded.Done()
				if kind == "disarmed" {
					g.Disarm()
				}
			case "gemini":
				g := newGeminiAttempt(ctx, &Account{ID: 7, Platform: PlatformGemini}, "gemini-2.5-flash", "streamGenerateContent", []byte(geminiTransportTestNativeBody), true, "gemini", start)
				defer g.close()
				done = g.ctx.Done()
			case "response_stage":
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
				stage := newAnthropicStreamStage(ctx, c, func() {})
				defer stage.close()
				st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 8, Model: "rebuilt", AttemptStart: time.Now()})
				select {
				case <-stage.timeout:
					stage.noteTimeout(c)
				case <-time.After(time.Second):
					t.Fatal("response-stage timer did not fire")
				}
			}
			if kind != "response_stage" {
				st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 8, Model: "rebuilt", AttemptStart: time.Now()})
				select {
				case <-done:
					require.NotEqual(t, "disarmed", kind)
				case <-time.After(160 * time.Millisecond):
					require.Equal(t, "disarmed", kind, "timer did not fire")
				}
			}
			var fired []resilience.BudgetEventForTest
			for _, e := range trace.Events() {
				if e.Kind == "timer_fired" {
					fired = append(fired, e)
				}
			}
			if kind == "disarmed" {
				require.Empty(t, fired)
				return
			}
			require.Len(t, fired, 1)
			require.EqualValues(t, 7, fired[0].AccountID)
			require.True(t, fired[0].DeadlineLimited)
			require.Equal(t, fired[0].Plan.Effective, fired[0].Plan.Remaining)
			require.EqualValues(t, 8, st.AttemptProfile().AccountID)
			require.NotContains(t, *st.TimingJSON(), "timer_fired")
		})
	}
}
