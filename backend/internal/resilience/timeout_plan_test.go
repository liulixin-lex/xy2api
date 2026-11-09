//go:build unit

package resilience

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimeoutPlanTierBoundaryAndSnapshot(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tier := 30 * time.Second
	for _, mode := range []string{BudgetAttributionOff, BudgetAttributionObserve, BudgetAttributionEnforce} {
		for _, last := range []bool{false, true} {
			for _, delta := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
				t.Run(fmt.Sprintf("%s/last=%t/delta=%s", mode, last, delta), func(t *testing.T) {
					cfg := Defaults()
					cfg.Budget.BudgetCutAttribution = mode
					b := NewBudget(context.Background(), &cfg, KindText, t0)
					start := t0.Add(240*time.Second - tier - delta)
					p := b.PlanAttemptTimeout(tier, last, start)
					require.Equal(t, tier+delta, p.Effective())
					require.Equal(t, delta < 0 && mode == BudgetAttributionEnforce, p.Applied())
					d := p.Diagnostics()
					require.Equal(t, mode != BudgetAttributionOff, d.WouldBudgetCut)
					require.Equal(t, delta >= 0 && mode != BudgetAttributionOff, d.CutAfterFullTier)
					cfg.Budget.BudgetCutAttribution = BudgetAttributionOff
					b.PlanAttemptTimeout(time.Second, last, start.Add(time.Second))
					require.Equal(t, d, p.Diagnostics(), "later plans/config cannot change a timer's plan")
				})
			}
		}
	}
}

func TestTimeoutPlanSourcesAndLegacyDurations(t *testing.T) {
	t0 := time.Unix(1000, 0)
	for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		cfg := Defaults()
		cfg.Budget.BudgetStart = mode
		cfg.Budget.BudgetCutAttribution = BudgetAttributionEnforce
		for _, spent := range []time.Duration{0, 10 * time.Second, 101 * time.Second, 120 * time.Second, 211 * time.Second, 241 * time.Second} {
			for _, tier := range []time.Duration{-1, 0, 30 * time.Second, 240 * time.Second} {
				for _, last := range []bool{false, true} {
					b := NewBudget(context.Background(), &cfg, KindText, t0)
					b.MarkBodyReceived(t0.Add(101*time.Second), "body_eof")
					start := t0.Add(spent)
					rem := b.Remaining(start)
					want := tier
					if last {
						want = rem
					} else {
						if tier > 0 && spent > tier {
							want = spent
						}
						if rem > 0 && (want <= 0 || rem < want) {
							want = rem
						}
					}
					p := b.PlanAttemptTimeout(tier, last, start)
					require.Equal(t, want, p.Effective(), "mode=%s spent=%s tier=%s last=%t", mode, spent, tier, last)
					if tier <= 0 {
						require.False(t, p.Applied())
					}
				}
			}
		}
	}

	cfg := Defaults()
	b := NewBudget(context.Background(), &cfg, KindText, t0)
	equal := b.PlanAttemptTimeout(30*time.Second, false, t0.Add(120*time.Second))
	require.Equal(t, "raised_spent", equal.Diagnostics().TimeoutSource)
	require.False(t, equal.Diagnostics().WouldBudgetCut, "remaining==planned is not a cut")
	require.True(t, equal.fullTier, "remaining==planned still gives a full tier")
	require.False(t, equal.Applied())
	require.False(t, equal.Diagnostics().CutAfterFullTier, "observation is would && full tier")
	var nilBudget *Budget
	require.Equal(t, time.Second, nilBudget.PlanAttemptTimeout(time.Second, false, t0).Effective())
}

func TestTimeoutPlanResponseStageAndZero(t *testing.T) {
	cfg := Defaults()
	cfg.Budget.BudgetCutAttribution = BudgetAttributionEnforce
	t0 := time.Unix(1000, 0)
	st := NewRequestState()
	st.SetBudget(NewBudget(context.Background(), &cfg, KindText, t0))
	p := st.ResponseStagePlan(t0.Add(238 * time.Second))
	require.Equal(t, 2*time.Second, p.Effective())
	require.Equal(t, "response_stage_remaining", p.Diagnostics().TimeoutSource)
	require.False(t, p.Applied())
	require.False(t, p.Diagnostics().WouldBudgetCut)
	require.False(t, p.Diagnostics().CutAfterFullTier)
	raw, err := json.Marshal(TimeoutPlan{}.Diagnostics())
	require.NoError(t, err)
	require.Equal(t, "{}", string(raw))
}
