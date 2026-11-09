//go:build unit

package service

// Regression coverage for untimed Gemini admission and shared timer ownership.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// For each guard shape, the witness used by Gemini in-place admission
// (geminiRetryHasTimer) must equal the classification observeDispatch records
// for the retry it admits, and an admitted retry under body_eof must either be
// covered by a live timer bounded by D or behave exactly like arrival.
func TestGeminiRetryWitnessMatchesDispatchUnderBodyEOF(t *testing.T) {
	body := []byte(geminiTransportTestNativeBody)
	type result struct {
		allowed  bool
		noTimer  int64
		clampMs  int64
		timerSet bool
	}
	run := func(t *testing.T, mode, shape string) result {
		withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.BudgetStart = mode })
		account := &Account{ID: 1, Platform: PlatformGemini}
		st := resilience.NewRequestState()
		parent := resilience.WithRequestState(context.Background(), st)
		now := time.Now()
		t0 := now.Add(-239 * time.Second)
		st.SetBudget(resilience.NewBudget(parent, resilience.Current(), resilience.KindText, t0))
		st.MarkBodyReceived(t0.Add(30*time.Second), "body_eof", int64(len(body)))
		if shape == "last_candidate" {
			st.SetLastResort(account.ID)
		}
		g := newGeminiAttempt(parent, account, "gemini-2.5-flash", "streamGenerateContent", body, true, "gemini", now)
		defer g.close()
		timerSet := g.hasTimer()
		ctx := g.ctx
		switch shape {
		case "completed":
			g.mu.Lock()
			g.timer.Stop()
			g.timer = nil
			g.mu.Unlock()
		case "output":
			g.mu.Lock()
			g.startOutput(false)
			g.mu.Unlock()
		case "parent_ctx":
			ctx = parent
		}
		clamp := st.Budget().ClampWaitWithTimer(10*time.Second, time.Now(), geminiRetryHasTimer(ctx))
		retries := 0
		allowed := geminiInPlaceRetryAllowed(ctx, &retries)
		before := *st.Timing().LateAttemptNoTimerCount
		if allowed {
			g.observeDispatch(time.Now())
		}
		after := *st.Timing().LateAttemptNoTimerCount
		return result{allowed: allowed, noTimer: after - before, clampMs: clamp.Milliseconds() / 1000, timerSet: timerSet}
	}
	for _, shape := range []string{"timed", "last_candidate", "completed", "output", "parent_ctx"} {
		t.Run(shape, func(t *testing.T) {
			arrival := run(t, resilience.BudgetStartArrival, shape)
			bodyEOF := run(t, resilience.BudgetStartBodyEOF, shape)
			t.Logf("REVIEW-B shape=%s arrival=%+v body_eof=%+v", shape, arrival, bodyEOF)
			require.False(t, arrival.allowed, "arrival at T0+239 rejects")
			if bodyEOF.allowed {
				require.Equal(t, "timed", shape)
				require.Zero(t, bodyEOF.noTimer, "admitted as timed => dispatch observed as timed")
			} else {
				require.Equal(t, fmt.Sprintf("%+v", result{false, 0, arrival.clampMs, bodyEOF.timerSet}),
					fmt.Sprintf("%+v", bodyEOF), "rejected untimed retry matches arrival")
			}
		})
	}
}

// Arrival byte-for-byte: the new admission helpers equal the old D view for
// every offset and timer witness, before/after EOF marking and for images.
func TestArrivalAdmissionMatchesRemaining(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, kind := range []resilience.RequestKind{resilience.KindText, resilience.KindImage} {
		for _, base := range []int{30, 60, 240, 290} {
			for _, eof := range []time.Duration{0, 5 * time.Second, 100 * time.Second} {
				cfg := resilience.Defaults()
				cfg.Budget.PreOutputBudgetSeconds = base
				cfg.Budget.BudgetStart = resilience.BudgetStartArrival
				b := resilience.NewBudget(context.Background(), &cfg, kind, t0)
				if eof > 0 {
					b.MarkBodyReceived(t0.Add(eof), "body_eof")
				}
				for off := -5 * time.Second; off <= time.Duration(base+5)*time.Second; off += 500 * time.Millisecond {
					at := t0.Add(off)
					for _, timer := range []bool{false, true} {
						require.Equal(t, b.Remaining(at), b.AdmissionRemaining(at, timer))
						require.Equal(t, b.ClampWait(7*time.Second, at), b.ClampWaitWithTimer(7*time.Second, at, timer))
					}
				}
			}
		}
	}
}
