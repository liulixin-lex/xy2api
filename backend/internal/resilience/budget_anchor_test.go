//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudgetAnchorThresholdAndFreeze(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, delta := range []time.Duration{0, time.Second - time.Nanosecond, time.Second, 30 * time.Second, 100 * time.Second, 296 * time.Second} {
		for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
			s := Defaults()
			s.Budget.BudgetStart = mode
			b := NewBudget(context.Background(), &s, KindText, t0)
			b.MarkBodyReceived(t0.Add(delta), "body_eof")
			anchor := t0
			if mode == BudgetStartBodyEOF && delta >= time.Second {
				anchor = t0.Add(delta)
			}
			require.Equal(t, anchor, b.DeadlineAnchor())
			require.Equal(t, minTime(anchor.Add(240*time.Second), t0.Add(PreOutputWallCap)), b.Deadline())
			b.MarkBodyReceived(t0.Add(400*time.Second), "handler_read")
			require.Equal(t, anchor, b.DeadlineAnchor())
			require.Equal(t, t0, b.Started())
			require.Equal(t, 60*time.Second, b.RecordAttemptAt(t0.Add(60*time.Second)))
		}
	}
	for _, freeze := range []string{"profile", "timer"} {
		s := Defaults()
		s.Budget.BudgetStart = BudgetStartBodyEOF
		b := NewBudget(context.Background(), &s, KindText, t0)
		st := NewRequestState()
		st.SetBudget(b)
		if freeze == "profile" {
			st.SetAttemptProfile(AttemptProfile{AttemptStart: t0.Add(time.Second)})
		} else {
			b.AttemptTimeout(time.Second, false, t0)
		}
		require.Zero(t, b.Attempts())
		b.MarkBodyReceived(t0.Add(100*time.Second), "body_eof")
		require.Equal(t, t0, b.DeadlineAnchor())
	}
	s := Defaults()
	s.Budget.BudgetStart = BudgetStartBodyEOF
	b := NewBudget(context.Background(), &s, KindImage, t0)
	b.MarkBodyReceived(t0.Add(100*time.Second), "body_eof")
	require.True(t, b.Deadline().IsZero())
	require.Equal(t, t0, b.DeadlineAnchor())
	var none *Budget
	none.MarkBodyReceived(t0, "body_eof")
	require.True(t, none.Deadline().IsZero())
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func TestBudgetAnchorDoesNotChangeArrivalBasedAttemptDurationU6(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, elapsed := range []time.Duration{60*time.Second - time.Nanosecond, 60 * time.Second} {
		var arrivalDuration time.Duration
		for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
			settings := Defaults()
			settings.Budget.BudgetStart = mode
			budget := NewBudget(context.Background(), &settings, KindText, t0)
			budget.MarkBodyReceived(t0.Add(100*time.Second), "body_eof")
			duration := budget.RecordAttemptAt(t0.Add(elapsed))
			require.Equal(t, elapsed, duration, "lastMark and attempt duration remain anchored to arrival")
			if mode == BudgetStartArrival {
				arrivalDuration = duration
			} else {
				require.Equal(t, arrivalDuration, duration)
			}
		}
	}
}

func TestBudgetAnchorLegacyValuesAndPrefix(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, base := range []int{60, 240, 290} {
		for _, upload := range []time.Duration{500 * time.Millisecond, time.Second, 30 * time.Second, 101 * time.Second, 296 * time.Second} {
			for _, elapsed := range []time.Duration{0, 3 * time.Second, 75 * time.Second, 140 * time.Second} {
				for _, tier := range []time.Duration{0, 20 * time.Second, 40 * time.Second, 75 * time.Second, 240 * time.Second} {
					for _, last := range []bool{false, true} {
						s := Defaults()
						s.Budget.PreOutputBudgetSeconds = base
						legacy := NewBudget(context.Background(), &s, KindText, t0)
						legacy.MarkBodyReceived(t0.Add(upload), "body_eof")
						now := t0.Add(upload + elapsed)
						rem := t0.Add(time.Duration(base) * time.Second).Sub(now)
						if rem <= 0 {
							rem = time.Nanosecond
						}
						want := tier
						if last {
							want = rem
						} else {
							if spent := now.Sub(t0); want > 0 && spent > want {
								want = spent
							}
							if rem > 0 && (want <= 0 || rem < want) {
								want = rem
							}
						}
						require.Equal(t, want, legacy.AttemptTimeout(tier, last, now))
						s.Budget.BudgetStart = BudgetStartBodyEOF
						eof := NewBudget(context.Background(), &s, KindText, t0)
						eof.MarkBodyReceived(t0.Add(upload), "body_eof")
						got := eof.AttemptTimeout(tier, last, now)
						require.GreaterOrEqual(t, got, want)
						planned := max(tier, now.Sub(t0))
						if !last && tier > 0 && rem >= planned {
							require.Equal(t, want, got)
						}
						require.GreaterOrEqual(t, eof.Remaining(now), legacy.Remaining(now))
						if legacy.CanAttempt(now) {
							require.True(t, eof.CanAttempt(now))
						}
					}
				}
			}
		}
	}
	// ER-2: warm 10 MiB replay still raises to arrival-spent, with either anchor.
	for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		s := Defaults()
		s.Budget.BudgetStart = mode
		b := NewBudget(context.Background(), &s, KindText, t0)
		b.MarkBodyReceived(t0.Add(101*time.Second), "body_eof")
		require.Equal(t, 101*time.Second, b.AttemptTimeout(40*time.Second, false, t0.Add(101*time.Second)))
	}
}

func TestBudgetAnchorSnapshotAndReceipt(t *testing.T) {
	s := Defaults()
	t0 := time.Unix(1700000000, 0)
	ctx := WithClientFamily(context.Background(), "codex/1.0")
	b := NewBudget(ctx, &s, KindText, t0)
	s.Budget.BudgetStart = BudgetStartBodyEOF
	s.Budget.BudgetCutAttribution = BudgetAttributionEnforce
	s.Budget.PreOutputBudgetSeconds = 60
	s.Budget.MaxAttempts = 1
	b.MarkBodyReceived(t0.Add(100*time.Second), "body_eof")
	require.Equal(t, t0, b.DeadlineAnchor())
	require.Equal(t, t0.Add(240*time.Second), b.Deadline())
	require.Equal(t, 4, b.MaxAttempts())
	require.Equal(t, BudgetAttributionObserve, b.attribution)
	require.Equal(t, "codex", b.clientFamily)
	for _, budget := range []*Budget{b, nil, NewBudget(ctx, &s, KindImage, t0)} {
		st := NewRequestState()
		st.SetBudget(budget)
		st.MarkFirstBodyRead(t0.Add(time.Second))
		st.MarkBodyReceived(t0.Add(2*time.Second), "body_eof", 123)
		st.MarkBodyReceived(t0.Add(3*time.Second), "preread", 999)
		st.MarkBodyReceived(t0.Add(4*time.Second), "handler_read", 1000)
		require.Equal(t, t0.Add(2*time.Second), st.BodyReceivedAt())
		require.Equal(t, int64(2), st.bodyReceiptRepeat)
		require.Equal(t, int64(123), st.bodyBytes)
		require.False(t, st.bodyFirstRead.After(st.bodyReceived))
	}
	parsed, err := Parse(`{"budget":{"enabled":true}}`)
	require.NoError(t, err)
	require.Equal(t, BudgetStartArrival, parsed.Budget.BudgetStart)
	require.Equal(t, BudgetAttributionObserve, parsed.Budget.BudgetCutAttribution)
	for _, raw := range []string{`{"budget":{"budget_start":"invalid"}}`, `{"budget":{"budget_cut_attribution":"invalid"}}`} {
		_, err = Parse(raw)
		require.Error(t, err)
	}
}
