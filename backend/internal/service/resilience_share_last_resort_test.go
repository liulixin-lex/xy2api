//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// Review F3: in a share group, last resort is the share plan run once more
// with soft-penalised accounts counted as eligible, so it follows priorities
// and the session binding like normal placement. Each case runs on both
// schedulers; on the original scheduler last resort keeps ordering by
// priority, then by the end of the penalty.

const (
	lrAccountA = int64(1)
	lrAccountB = int64(2)
	lrSession  = "sess-last-resort"
)

// lastResortSchedulers lists the schedulers each case runs on.
var lastResortSchedulers = []sharesched.Scheduler{sharesched.SchedulerSub2API, sharesched.SchedulerShare}

// newGatewayLastResortEnv builds a group of accounts A and B with the given
// priorities, both in a soft cooldown.
func newGatewayLastResortEnv(t *testing.T, scheduler sharesched.Scheduler, priorityA, priorityB int) *shareTestEnv {
	t.Helper()
	withResilienceSettings(t, nil)
	env := newShareTestEnv(t, shareTestPolicy(scheduler),
		Account{ID: lrAccountA, Priority: priorityA}, Account{ID: lrAccountB, Priority: priorityB})
	now := time.Now()
	softPenalise(env.repo.accountsByID[lrAccountA], now)
	softPenalise(env.repo.accountsByID[lrAccountB], now)
	env.gateway.accountRepo = groupListingRepo{env.repo}
	env.gateway.resilience = NewResilienceService(nil, newMemResilienceStore())
	return env
}

// selectGatewayLastResort selects for the session and applies the
// handler's admission binding, as the gateway handlers do.
func selectGatewayLastResort(t *testing.T, env *shareTestEnv) (*AccountSelectionResult, *resilience.RequestState) {
	t.Helper()
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(cacheableContext(), st)
	gid := shareTestGroupID
	result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, lrSession, "claude-sonnet-4-5", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result.Account)
	require.Equal(t, result.Account.ID, st.LastResort(), "served as the last resort")
	admission := ContextWithSelectionProfitGate(ctx, result)
	require.NoError(t, env.gateway.BindStickySessionAfterProfitAdmission(admission, &gid, lrSession, result.Account.ID))
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
	return result, st
}

func TestResilienceLastResortFollowsPriorities(t *testing.T) {
	// A has priority 5, B priority 1: both schedulers prefer B.
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, scheduler, 5, 1)
			result, _ := selectGatewayLastResort(t, env)
			require.Equal(t, lrAccountB, result.Account.ID)
		})
	}
}

func TestResilienceLastResortKeepsSoftBinding(t *testing.T) {
	// The session is bound to A, which is only soft-penalised; B has the
	// better priority. Whoever serves, the binding stays on A.
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, scheduler, 5, 1)
			env.cache.bind(lrSession, lrAccountA, time.Hour-time.Minute)
			result, _ := selectGatewayLastResort(t, env)
			if scheduler == sharesched.SchedulerShare {
				require.Equal(t, lrAccountA, result.Account.ID, "the bound account serves while it is eligible in the relaxed round")
			} else {
				require.Equal(t, lrAccountB, result.Account.ID, "the original order is by priority")
			}
			require.Equal(t, lrAccountA, env.cache.sessionBindings[lrSession], "the soft-penalised owner keeps the binding")
		})
	}
	t.Run("share/bound_busy", func(t *testing.T) {
		env := newGatewayLastResortEnv(t, sharesched.SchedulerShare, 5, 1)
		env.cache.bind(lrSession, lrAccountA, time.Hour-time.Minute)
		env.conc.busy[lrAccountA] = -1
		result, _ := selectGatewayLastResort(t, env)
		require.Equal(t, lrAccountB, result.Account.ID)
		require.Equal(t, lrAccountA, env.cache.sessionBindings[lrSession], "spilling to B leaves the binding on A")
		require.Empty(t, env.cache.replaced)
	})
}

func TestResilienceLastResortRebindsFromHardBoundAccount(t *testing.T) {
	// The bound account is out of balance (hard): the session moves.
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, scheduler, 1, 5)
			until := time.Now().Add(time.Hour)
			a := env.repo.accountsByID[lrAccountA]
			a.TempUnschedulableUntil, a.TempUnschedulableReason = &until, resilience.ReasonPrefixBalanceExhausted+": HTTP 402"
			env.cache.bind(lrSession, lrAccountA, time.Hour-time.Minute)
			result, _ := selectGatewayLastResort(t, env)
			require.Equal(t, lrAccountB, result.Account.ID)
			require.Equal(t, lrAccountB, env.cache.sessionBindings[lrSession])
		})
	}
}

func TestResilienceLastResortRebindsFromAvoidedAccount(t *testing.T) {
	// The session avoids its healthy bound account A after a failure
	// streak; B is soft-penalised and preferred by priority. B serves and
	// takes the binding.
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, scheduler, 5, 1)
			a := env.repo.accountsByID[lrAccountA]
			a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
			r := env.gateway.resilience
			require.NoError(t, r.store.AddSessionAvoid(t.Context(), shareTestGroupID, lrSession, lrAccountA, time.Minute))
			r.avoidingSessions.Store(sessionMarkKey(shareTestGroupID, lrSession), time.Now().Add(time.Minute))
			env.cache.bind(lrSession, lrAccountA, time.Hour-time.Minute)
			result, _ := selectGatewayLastResort(t, env)
			require.Equal(t, lrAccountB, result.Account.ID)
			require.Equal(t, lrAccountB, env.cache.sessionBindings[lrSession], "the binding leaves the avoided account")
		})
	}
}

func TestResilienceLastResortGrantsStickyAllowanceToBoundAccount(t *testing.T) {
	// The RPM and window-cost gates admit the session's bound account in
	// their sticky-only zone. A share session's owner comes from the share
	// binding: the share hook returns before the original scheduler notes
	// the sticky owner.
	zones := map[string]struct {
		extra map[string]any
		setup func(*GatewayService, *resilienceGateLimits)
	}{
		"rpm": {map[string]any{"base_rpm": 10, "rpm_sticky_buffer": 5},
			func(_ *GatewayService, l *resilienceGateLimits) { l.rpm[gateTestAccount] = 12 }},
		"window_cost": {map[string]any{"window_cost_limit": 10.0},
			func(_ *GatewayService, l *resilienceGateLimits) { l.windowCost[gateTestAccount] = 15 }},
	}
	for name, zone := range zones {
		stickyOnly := func(a *Account, now time.Time) {
			a.Type, a.Extra = AccountTypeOAuth, zone.extra
			softPenalise(a, now)
		}
		for _, scheduler := range lastResortSchedulers {
			t.Run(name+"/"+string(scheduler)+"/bound", func(t *testing.T) {
				env := newResilienceGateEnv(t, scheduler, stickyOnly, zone.setup)
				env.cache.bind(gateTestSession, gateTestAccount, time.Hour-time.Minute)
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(cacheableContext(), st)
				gid := shareTestGroupID
				result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, gateTestSession, gateTestModel, nil, "", 0)
				require.NoError(t, err)
				require.Equal(t, gateTestAccount, result.Account.ID)
				require.Equal(t, gateTestAccount, st.LastResort())
				admission := ContextWithSelectionProfitGate(ctx, result)
				require.NoError(t, env.gateway.BindStickySessionAfterProfitAdmission(admission, &gid, gateTestSession, result.Account.ID))
				require.Equal(t, gateTestAccount, env.cache.sessionBindings[gateTestSession], "the binding stays")
				if scheduler == sharesched.SchedulerShare {
					require.Empty(t, env.cache.replaced)
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
			t.Run(name+"/"+string(scheduler)+"/unbound", func(t *testing.T) {
				env := newResilienceGateEnv(t, scheduler, stickyOnly, zone.setup)
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(cacheableContext(), st)
				gid := shareTestGroupID
				_, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, gateTestSession, gateTestModel, nil, "", 0)
				require.ErrorIs(t, err, ErrNoAvailableAccounts, "a new session gets no sticky allowance")
			})
		}
	}
}

// newOpenAILastResortEnv is newGatewayLastResortEnv for the OpenAI scheduler.
func newOpenAILastResortEnv(t *testing.T, scheduler sharesched.Scheduler, priorityA, priorityB int) *openAIShareTestEnv {
	t.Helper()
	withResilienceSettings(t, nil)
	env := newOpenAIShareTestEnv(t, shareTestPolicy(scheduler), lrAccountA, lrAccountB)
	now := time.Now()
	env.account(lrAccountA).Priority = priorityA
	env.account(lrAccountB).Priority = priorityB
	softPenalise(env.account(lrAccountA), now)
	softPenalise(env.account(lrAccountB), now)
	env.svc.accountRepo = openAIGroupListingRepo{env.repo}
	env.svc.resilience = NewResilienceService(nil, newMemResilienceStore())
	return env
}

func selectOpenAILastResort(t *testing.T, env *openAIShareTestEnv) *AccountSelectionResult {
	t.Helper()
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(cacheableContext(), st)
	gid := shareTestGroupID
	sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel.Account)
	require.Equal(t, sel.Account.ID, st.LastResort(), "served as the last resort")
	admission := ContextWithSelectionProfitGate(ctx, sel)
	require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(admission, &gid, lrSession, sel.Account.ID))
	if sel.ReleaseFunc != nil {
		sel.ReleaseFunc()
	}
	return sel
}

func TestResilienceOpenAILastResortFollowsPriorities(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newOpenAILastResortEnv(t, scheduler, 5, 1)
			require.Equal(t, lrAccountB, selectOpenAILastResort(t, env).Account.ID)
		})
	}
}

func TestResilienceOpenAILastResortKeepsSoftBinding(t *testing.T) {
	const key = "openai:" + lrSession
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newOpenAILastResortEnv(t, scheduler, 5, 1)
			if scheduler == sharesched.SchedulerSub2API {
				// The advanced scheduler is where the original scheduler keeps
				// a soft-penalised binding (design §4.2).
				env.svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			}
			env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
			sel := selectOpenAILastResort(t, env)
			if scheduler == sharesched.SchedulerShare {
				require.Equal(t, lrAccountA, sel.Account.ID, "the bound account serves while it is eligible in the relaxed round")
			} else {
				require.Equal(t, lrAccountB, sel.Account.ID, "the original order is by priority")
			}
			require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "the soft-penalised owner keeps the binding")
		})
	}
	t.Run("share/bound_busy", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, sharesched.SchedulerShare, 5, 1)
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		env.conc.busy[lrAccountA] = -1
		sel := selectOpenAILastResort(t, env)
		require.Equal(t, lrAccountB, sel.Account.ID)
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "spilling to B leaves the binding on A")
		require.Empty(t, env.cache.replaced)
	})
}
