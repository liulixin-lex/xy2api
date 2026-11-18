//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFullFirstOutputTierUsesRawDuration(t *testing.T) {
	start := time.Now()
	tier := 30 * time.Second
	for _, mode := range []string{BudgetAttributionObserve, BudgetAttributionEnforce} {
		cfg := Defaults()
		cfg.Budget.BudgetCutAttribution = mode
		for _, last := range []bool{false, true} {
			for _, delta := range []time.Duration{-1, 0, 1} {
				budget := NewBudget(context.Background(), &cfg, KindText, start)
				at := start.Add(time.Duration(cfg.Budget.PreOutputBudgetSeconds)*time.Second - tier - delta)
				plan := budget.PlanAttemptTimeout(tier, last, at)
				require.Equal(t, tier+delta, plan.Effective())
				require.Equal(t, delta >= 0, plan.FullFirstOutputTier(), "%s last=%t delta=%s", mode, last, delta)
			}
		}
	}
	require.False(t, TimeoutPlan{}.FullFirstOutputTier())
}

func TestOwnerCutoffClearedOnNextGatewayTurn(t *testing.T) {
	st := NewRequestState()
	st.SetSession(7, "session", nil)
	st.SetSchedSelection(SchedSelection{AccountID: 2, StickyWarm: true, HTTPStreamText: true})
	require.True(t, st.OwnerHTTPDispatchPending(2))
	seq := st.MarkOpsUpstreamSendBoundary(2)
	fact := OwnerFirstOutputCutoff{AttemptSeq: seq, AccountID: 2, Generation: "first", CanTransfer: true}
	require.True(t, st.RecordOwnerCutoff(fact))
	require.False(t, st.RecordOwnerCutoff(OwnerFirstOutputCutoff{AttemptSeq: seq, AccountID: 9}))
	_, ok := st.ConfirmedOwnerCutoff()
	require.False(t, ok)
	require.False(t, st.ConfirmOwnerAvoid(seq+1, "first"))
	require.False(t, st.ConfirmOwnerAvoid(seq, "stale"))
	require.True(t, st.ConfirmOwnerAvoid(seq, "first"))
	got, ok := st.ConfirmedOwnerCutoff()
	require.True(t, ok)
	require.Equal(t, fact, got)
	st.BeginGatewayTurn(time.Now())
	_, ok = st.OwnerCutoff()
	require.False(t, ok)
	_, ok = st.ConfirmedOwnerCutoff()
	require.False(t, ok)
	require.False(t, st.ConfirmOwnerAvoid(seq, "first"))
	require.False(t, st.RecordOwnerCutoff(fact))
	require.False(t, st.OwnerHTTPDispatchPending(2))
	sel, _ := st.SchedSelection()
	require.False(t, sel.HTTPStreamText)
	next := st.MarkOpsUpstreamSendBoundary(2)
	require.Greater(t, next, seq)
	require.True(t, st.FirstHTTPDispatch(next))
	require.False(t, st.RecordOwnerCutoff(fact), "old turn after the next physical send")
	sameBinding := fact
	sameBinding.AttemptSeq = next
	require.True(t, st.RecordOwnerCutoff(sameBinding))
	require.False(t, st.ConfirmOwnerAvoid(seq, fact.Generation))
	_, ok = st.ConfirmedOwnerCutoff()
	require.False(t, ok)
	require.True(t, st.ConfirmOwnerAvoid(next, fact.Generation))
}

func TestOwnerCutoffAvoidReceiptIsIndependentOfTransfer(t *testing.T) {
	st := NewRequestState()
	seq := st.MarkOpsUpstreamSendBoundary(4)
	fact := OwnerFirstOutputCutoff{GroupID: 7, AccountID: 4, AttemptSeq: seq, Generation: "v2", CanTransfer: false}
	require.True(t, st.RecordOwnerCutoff(fact))
	require.False(t, st.ConfirmOwnerAvoid(seq, "stale"))
	require.True(t, st.ConfirmOwnerAvoid(seq, "v2"))
	got, ok := st.ConfirmedOwnerCutoff()
	require.True(t, ok)
	require.Equal(t, fact, got)
}
