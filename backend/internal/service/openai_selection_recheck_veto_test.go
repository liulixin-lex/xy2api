//go:build unit

package service

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestConstrainedImmediateVetoRetiresTrial(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	eligible := true
	ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, "m", 99, func(a *Account) bool { return a.ID != 1 || eligible })
	first, err := selectOrdinaryNewSessionForTest(t, f, ctx, "turn", nil, true)
	require.NoError(t, err)
	require.True(t, first.Acquired)
	require.Nil(t, first.WaitPlan)
	require.EqualValues(t, 1, first.Account.ID)
	require.Len(t, leases.held, 1)
	eligible = false
	_, deny, reason := f.svc.SelectionAdmissionForResult(ContextWithSelectionProfitGate(ctx, first), first, false)
	require.True(t, deny)
	require.Equal(t, "request_candidate_constraint", reason)
	CombineSelectionRelease(first, nil)()
	ch := pendingOpenAIStickyChange(ctx)
	target := int64(0)
	if ch != nil {
		target = ch.target
	}
	t.Logf("IMMEDIATE after_veto reason=%s acquired=%t slot_releases=%v trial_held=%d plan_target=%d", reason, first.Acquired, f.releases, len(leases.held), target)
	require.Zero(t, target)
	require.Empty(t, f.svc.stickyChangeAvoid(ctx, ptrInt64Selection(7), "turn"))
	require.Empty(t, leases.held)
	next, err := selectOrdinaryNewSessionForTest(t, f, ctx, "turn", nil, true)
	require.NoError(t, err)
	require.EqualValues(t, 2, next.Account.ID)
	next.ReleaseFunc()
	CompleteOpenAIStickyChange(ctx, 2, true, false)
	t.Logf("IMMEDIATE after_next owner=%d slot_releases=%v trial_held=%d", f.cache.sessionBindings["openai:turn"], f.releases, len(leases.held))
	require.EqualValues(t, 2, f.cache.sessionBindings["openai:turn"])
	require.Equal(t, []int64{1, 2}, f.releases)
	require.Empty(t, leases.held)
}

func TestConstrainedVetoKeepsLaterPlanForSameAccount(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	allowed := true
	ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, "m", 99, func(*Account) bool { return allowed })
	selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "turn", nil, true)
	require.NoError(t, err)
	defer selected.ReleaseFunc()
	old := pendingOpenAIStickyChange(ctx)
	require.NotNil(t, old)
	next := old.replacement("create", "m", selected.Account.ID, "")
	resilience.StateFrom(ctx).SetStickyChange(next)
	allowed = false
	_, veto, _ := f.svc.SelectionAdmissionForResult(ContextWithSelectionProfitGate(ctx, selected), selected, false)
	require.True(t, veto)
	require.Empty(t, leases.held)
	require.Same(t, next, pendingOpenAIStickyChange(ctx))
	CompleteOpenAIStickyChange(ctx, selected.Account.ID, true, false)
	require.EqualValues(t, selected.Account.ID, f.cache.sessionBindings["openai:turn"])
}
