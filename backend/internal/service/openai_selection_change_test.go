//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestStickyChangeCommitsOnlySuccessfulTarget(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel", "conflict", "error"} {
		t.Run(outcome, func(t *testing.T) {
			f := selectionNewSessionFixture()
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			require.Equal(t, int64(10), f.binding.owner, "selection must preserve the original owner")
			require.Empty(t, f.binding.casKeys)
			require.True(t, preservePendingOpenAIStickyBinding(ctx, ptrInt64Selection(7), "session"))
			CompleteOpenAIStickyChange(ctx, 99, true, false)
			require.Empty(t, f.binding.casKeys, "an unrelated attempt cannot complete the pending target")
			f.binding.casFail = outcome == "conflict"
			f.binding.casError = outcome == "error"
			success := outcome != "failure" && outcome != "cancel"
			CompleteOpenAIStickyChange(ctx, selected.Account.ID, success, outcome == "cancel")
			CompleteOpenAIStickyChange(ctx, selected.Account.ID, !success, false)
			if outcome == "success" {
				require.Equal(t, selected.Account.ID, f.binding.owner)
				require.Equal(t, []string{"sticky_change:session"}, f.binding.avoidScopes)
			} else {
				require.Equal(t, int64(10), f.binding.owner)
			}
			avoid, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
			require.NoError(t, err)
			if outcome == "failure" {
				require.Contains(t, avoid, selected.Account.ID)
			} else {
				require.Empty(t, avoid)
			}
			nextCtx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			f.candidates = f.candidates[:1]
			if outcome == "failure" {
				require.Nil(t, f.session.selectOwner(nextCtx, 7, "session", "m", f.owner, f.candidates, selectionRequest{}))
			}
		})
	}
}

func TestAbandonOpenAIStickyForwardForIdentityConflict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     string
		reason   string
		request  bool
		withPlan bool
	}{
		{name: "takeover plan", kind: "takeover", reason: "priority_takeover", withPlan: true},
		{name: "borrow plan", kind: "owner_released", reason: "borrowed", request: true, withPlan: true},
		{name: "ordinary request", withPlan: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			target := f.candidates[0]
			var leases *selectionTrialLeaseFake
			if tc.withPlan {
				leases = newSelectionTrialLeaseFake()
				member := TrialLeaseRequestMember("identity-conflict-attempt")
				acquired, _, err := leases.Acquire(ctx, target.ID, "m", 7, member, time.Now())
				require.NoError(t, err)
				require.True(t, acquired)
				state.SetStickyChange(&openAIStickyChange{
					groupID: 7, session: "session", key: f.binding.key, model: "m", kind: tc.kind,
					reason: tc.reason, expected: StickySessionBinding{AccountID: f.owner.ID, Generation: "g1"},
					target: target.ID, ownerReleased: true, ttl: time.Hour, cache: f.binding,
					trial: &selectionTrialReservation{leases: leases, account: target.ID, group: 7, model: "m", member: member, request: tc.request},
				})
			}

			AbandonOpenAIStickyForwardForIdentityConflict(ctx, target.ID)

			require.Equal(t, f.owner.ID, f.binding.owner, "abandonment preserves the prior binding")
			require.Empty(t, f.binding.casKeys)
			require.False(t, state.Avoided(target.ID))
			avoided, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
			require.NoError(t, err)
			require.Empty(t, avoided)
			if !tc.withPlan {
				require.Nil(t, pendingOpenAIStickyChange(ctx))
				return
			}
			require.Empty(t, leases.held, "abandonment releases this attempt's trial reservation")
			require.Nil(t, pendingOpenAIStickyChange(ctx), "the old plan must not block the next candidate")

			next := f.candidates[1]
			state.SetStickyChange(&openAIStickyChange{
				groupID: 7, session: "session", key: f.binding.key, model: "m", kind: "owner_released",
				expected: StickySessionBinding{AccountID: f.owner.ID, Generation: "g1"},
				target:   next.ID, ownerReleased: true, ttl: time.Hour, cache: f.binding,
			})
			CompleteOpenAIStickyChange(ctx, next.ID, true, false)
			require.Equal(t, next.ID, f.binding.owner, "the next candidate still settles normally")
		})
	}
}

func TestStickyChangeAdmissionAndFailureKeepOriginalBinding(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
	f.cache.sessionBindings["openai:session"] = 10
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	group := int64(7)
	result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID)
	result.ReleaseFunc()
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, &group, "session", 1))
	require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
	CompleteOpenAIStickyChange(ctx, 1, false, false)
	// Legacy cleanup is unconditionally deleting without an owner CAS. A
	// failed target must not erase the original binding through that path.
	require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, &group, "session", 1))
	require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
	next, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "session", "m", map[int64]struct{}{1: {}}, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, int64(10), next.Account.ID)
	next.ReleaseFunc()
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, &group, "session", 10))
	require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
}

func TestStickyChangeRequiresNormalTargetInSameGroup(t *testing.T) {
	for _, change := range []string{"wrong_group", "removed_during_acquire", "failing_in_this_group"} {
		t.Run(change, func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.candidates = f.candidates[:1]
			switch change {
			case "wrong_group":
				f.candidates[0].GroupIDs = []int64{8}
			case "removed_during_acquire":
				f.slots.after = func(int64) { f.candidates[0].GroupIDs = []int64{8} }
			case "failing_in_this_group":
				f.state(1, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear, Until: f.now.Add(time.Minute)}})
			}
			require.Nil(t, f.selectNow(context.Background()))
			require.Equal(t, int64(10), f.binding.owner)
			require.Zero(t, f.slots.held)
		})
	}
}

func TestStickyChangeDisabledOwnerCommitsOnlyAfterReplacementSucceeds(t *testing.T) {
	for _, successful := range []bool{false, true} {
		t.Run(map[bool]string{true: "success", false: "failure"}[successful], func(t *testing.T) {
			owner := selectionFixtureAccount(10, 5)
			owner.Schedulable = false
			f := newSelectionRuntimeFixture(t, owner, selectionFixtureAccount(1, 1))
			f.cache.sessionBindings["openai:session"] = 10
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			group := int64(7)
			result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, result)
			defer result.ReleaseFunc()
			require.Equal(t, int64(1), result.Account.ID)
			require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
			require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, &group, "session", 1))
			require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
			CompleteOpenAIStickyChange(ctx, 1, successful, false)
			if successful {
				require.Equal(t, int64(1), f.cache.sessionBindings["openai:session"])
			} else {
				require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
			}
		})
	}
}

func TestFailedChangeTargetCannotReturnThroughLastResort(t *testing.T) {
	withResilienceSettings(t, nil)
	owner := selectionFixtureAccount(10, 5)
	owner.Schedulable = false
	f := newSelectionRuntimeFixture(t, owner, selectionFixtureAccount(1, 1))
	f.cache.sessionBindings["openai:session"] = 10
	group := int64(7)
	require.NoError(t, f.svc.resilience.store.AddSessionAvoid(context.Background(), group, stickyChangeAvoidSession("session"), 1, openAIStickyChangeAvoidTTL))
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.Error(t, err)
	require.Nil(t, selected, "a failed change target is unavailable even when all other accounts are down")
	require.Empty(t, f.attempts)
}
