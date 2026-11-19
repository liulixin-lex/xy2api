//go:build unit

package service

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Interrupt exactly one policy read on the return path, after B was released.
// This exercises the same public call for both sides of the finishSelectionSlot return.
func TestNewPickPanicOnReturn(t *testing.T) {
	for _, site := range []string{"finishSelectionSlot", "(*selectionTrial).finish"} {
		t.Run(site, func(t *testing.T) {
			f, _ := newPickFixture(t)
			logs := captureSchedObsLogs(t)
			fired := false
			f.svc.selectionBoard.view = func() schedquality.View {
				if !fired && len(f.releases) == 1 && f.releases[0] == 1 {
					if newPickCalledFrom(site) {
						fired = true
						panic("review: policy view interrupted")
					}
				}
				return f.view
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			require.PanicsWithValue(t, "review: policy view interrupted", func() { selectTrialRuntime(t, f, ctx, "late-panic") })
			picked := 0
			for _, e := range schedObsLogRecords(t, logs) {
				if e["msg"] == "sched_quality.new_pick" && e["result"] == "picked" {
					picked++
				}
			}
			c := f.svc.selectionBoard.newPickLimit.counts[2]
			require.ElementsMatch(t, []int64{1, 2}, f.releases)
			require.Equal(t, newPickCount{}, c)
			require.Zero(t, picked)
		})
	}
}

func TestNewPickPublicGuard(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"ordinary avoid", "first-output avoid", "monitor fallback"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newPickFixture(t)
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now(), resilience.GatewayRequestScopeText)
			ctx := resilience.WithRequestState(context.Background(), st)
			key := "session"
			switch mode {
			case "ordinary avoid", "first-output avoid":
				if mode == "first-output avoid" {
					key = firstOutputAvoidSession(key)
				}
				require.NoError(t, f.svc.resilience.store.AddSessionAvoid(ctx, 7, key, 2, time.Minute))
				ids, err := f.svc.resilience.store.SessionAvoidWrittenAt(ctx, 7, key)
				require.NoError(t, err)
				require.Contains(t, ids, int64(2))
			case "monitor fallback":
				ctx = monitorprobe.WithProbe(ctx, 1)
				f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 100}, 2: {LoadRate: 100}, 3: {LoadRate: 100}}
			}
			r, err := f.svc.SelectAccountWithLoadAwareness(ctx, ptrInt64Selection(7), "session", "m", nil)
			require.NoError(t, err)
			require.NotNil(t, r)
			if r.ReleaseFunc != nil {
				defer r.ReleaseFunc()
			}
			c := f.svc.selectionBoard.newPickLimit.counts[2]
			require.EqualValues(t, 1, r.Account.ID)
			require.Equal(t, newPickCount{}, c)
		})
	}
}

func TestNewPickOriginalSuffixAfterTrialRequeue(t *testing.T) {
	f, s := newPickFixture(t)
	s.scores = map[int64]int64{1: 0, 2: 30_000}
	leases := newSelectionTrialLeaseFake()
	leases.lose[1] = true
	selectionSetTrial(f, 1, leases)
	f.loads.after = func(id int64) {
		if id == 2 {
			// The earlier trial row is already requeued behind the real front.
			// A new view now promotes that row to the real front.
			delete(f.view.States, fakeSchedViewKey{7, 1, "m"})
		}
	}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	r := selectTrialRuntime(t, f, ctx, "earlier-trial")
	defer r.ReleaseFunc()
	c := f.svc.selectionBoard.newPickLimit.counts[1]
	require.EqualValues(t, 2, r.Account.ID)
	require.Equal(t, []int64{1, 2}, f.attempts)
	require.Equal(t, newPickCount{}, c)
}

func newPickCalledFrom(site string) bool {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, site) {
			return true
		}
		if !more {
			return false
		}
	}
}

func TestNewPickDeliveryAfterDecisionWindow(t *testing.T) {
	for _, mode := range []string{"deliver", "cancel", "panic at outer return"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newPickFixture(t)
			logs := captureSchedObsLogs(t)
			old := s.at.UTC().Truncate(15 * time.Minute)
			now := old.Add(15*time.Minute - time.Second)
			s.at, f.view.ViewAt = now, now
			f.svc.selectionBoard.now = func() time.Time { return now }
			ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
			defer cancel()
			l := &f.svc.selectionBoard.newPickLimit
			var decisionCounts map[int64]newPickCount
			f.svc.selectionBoard.view = func() schedquality.View {
				if decisionCounts == nil && len(f.releases) == 1 && newPickCalledFrom("(*selectionTrial).finish") {
					decisionCounts = l.counts
					require.Equal(t, newPickCount{pending: 1}, decisionCounts[2])
					now = now.Add(time.Second)
					l.mu.Lock()
					l.advance(now)
					l.mu.Unlock()
					if mode == "cancel" {
						cancel()
					}
				}
				if decisionCounts != nil && mode == "panic at outer return" && newPickCalledFrom("(*OpenAIGatewayService).recordOpenAISelectionFlags") {
					panic("outer return")
				}
				return f.view
			}
			call := func() (*AccountSelectionResult, error) {
				r, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "late", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				return r, err
			}
			if mode == "panic at outer return" {
				require.PanicsWithValue(t, "outer return", func() { _, _ = call() })
			} else {
				r, err := call()
				if mode == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
					require.Nil(t, r)
				} else {
					require.NoError(t, err)
					require.EqualValues(t, 2, r.Account.ID)
					require.Equal(t, []int64{1}, f.releases)
					r.ReleaseFunc()
				}
			}
			require.NotNil(t, decisionCounts)
			require.Equal(t, newPickCount{}, l.counts[2])
			require.Equal(t, []int64{1, 2}, f.attempts)
			require.ElementsMatch(t, []int64{1, 2}, f.releases)
			picked := 0
			for _, e := range schedObsLogRecords(t, logs) {
				if e["msg"] == "sched_quality.new_pick" && e["result"] == "picked" {
					picked++
					require.Equal(t, old.Format(time.RFC3339Nano), e["window"])
				}
			}
			want := 0
			if mode == "deliver" {
				want = 1
			}
			require.Equal(t, newPickCount{picked: want}, decisionCounts[2])
			require.Equal(t, want, picked)
		})
	}
}
