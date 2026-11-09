//go:build unit

package resilience

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUntimedAdmissionMatchesArrival(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, base := range []int{60, 240, 290} {
		for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond, 3 * time.Second} {
			t.Run(fmt.Sprintf("base=%d/offset=%s", base, offset), func(t *testing.T) {
				var results [2]string
				for i, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
					cfg := Defaults()
					cfg.Budget.PreOutputBudgetSeconds = base
					cfg.Budget.BudgetStart = mode
					b := NewBudget(context.Background(), &cfg, KindText, t0)
					b.MarkBodyReceived(t0.Add(30*time.Second), "body_eof")
					at := t0.Add(time.Duration(base)*time.Second - 2*time.Second + offset)
					allowed := b.CanAttempt(at)
					exhausted := b.Exhausted()
					wait := b.ClampWait(10*time.Second, at)
					consumed := b.ConsumeInPlaceRetry(7, true, at)
					results[i] = fmt.Sprintf("can=%t exhausted=%s wait=%s consume=%t attempts=%d", allowed, exhausted, wait, consumed, b.Attempts())
				}
				t.Logf("BEHAVIOR arrival=%s body_eof=%s", results[0], results[1])
				require.Equal(t, results[0], results[1])
			})
		}
	}
}

func TestUntimedAdmissionDoesNotBorrowPreviousTimer(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	cfg := Defaults()
	cfg.Budget.BudgetStart = BudgetStartBodyEOF
	st := NewRequestState()
	b := NewBudget(context.Background(), &cfg, KindText, t0)
	st.SetBudget(b)
	st.MarkBodyReceived(t0.Add(30*time.Second), "body_eof", 1)
	start := t0.Add(237 * time.Second)
	st.SetAttemptProfile(AttemptProfile{AccountID: 7, AttemptStart: start, Stream: true})
	plan := st.PlanAttemptTimeout(60*time.Second, false, start)
	require.Equal(t, 33*time.Second, plan.Effective(), "the installed timer still uses D")
	require.False(t, b.CanAttempt(t0.Add(239*time.Second)), "the next selected account has no known timer yet")
	require.Equal(t, ExhaustedDeadline, b.Exhausted())
}

func TestTimedAdmissionUsesDAndArrivalIsUnchanged(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	at := t0.Add(239 * time.Second)
	for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		cfg := Defaults()
		cfg.Budget.BudgetStart = mode
		b := NewBudget(context.Background(), &cfg, KindText, t0)
		b.MarkBodyReceived(t0.Add(30*time.Second), "body_eof")
		require.Equal(t, b.Remaining(at), b.AdmissionRemaining(at, true))
		if mode == BudgetStartBodyEOF {
			require.Equal(t, 31*time.Second, b.Remaining(at))
			require.Equal(t, 10*time.Second, b.ClampWaitWithTimer(10*time.Second, at, true))
			require.True(t, b.ConsumeInPlaceRetryWithTimer(7, false, at, true))
		} else {
			require.Equal(t, b.ClampWait(10*time.Second, at), b.ClampWaitWithTimer(10*time.Second, at, true))
			require.False(t, b.ConsumeInPlaceRetryWithTimer(7, false, at, true))
		}
	}
	for _, kind := range []RequestKind{KindText, KindImage} {
		cfg := Defaults()
		b := NewBudget(context.Background(), &cfg, kind, t0)
		if kind == KindImage {
			require.Zero(t, b.AdmissionRemaining(at, false))
			require.True(t, b.CanAttempt(at))
		}
	}
	var none *Budget
	require.Zero(t, none.AdmissionRemaining(at, false))
	require.True(t, none.ConsumeInPlaceRetryWithTimer(0, false, at, false))
}
