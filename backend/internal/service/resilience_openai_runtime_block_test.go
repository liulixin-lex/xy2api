//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// An OpenAI account held back only by the scheduler's in-process transient
// breaker (upstream 5xx in a row block the model for 10 s, then 45 s) is
// soft-penalised (design §2): when no other account can serve, last resort
// uses it instead of answering "no available accounts". On the staging
// server a single-account group whose relay answered 503 was unavailable
// for 45 s, and its 503s no longer counted as group-level no-account.

func newOpenAIRuntimeBlockEnv(t *testing.T, scheduler sharesched.Scheduler, ids ...int64) *openAIShareTestEnv {
	t.Helper()
	withResilienceSettings(t, nil)
	rules := make([]shareRule, 0, len(ids))
	for _, id := range ids {
		rules = append(rules, shareRule{AccountID: id, Tier: 0, Weight: 1})
	}
	env := newOpenAIShareTestEnv(t, shareTestPolicy(scheduler, rules...), ids...)
	env.svc.accountRepo = openAIGroupListingRepo{env.repo}
	env.svc.resilience = NewResilienceService(nil, newMemResilienceStore())
	return env
}

func breakOpenAIModel(t *testing.T, env *openAIShareTestEnv, id int64, failures int, at time.Time) {
	t.Helper()
	for i := 0; i < failures; i++ {
		env.svc.recordOpenAIAccountModelTransientFailure(env.account(id), "gpt-5.1", at)
	}
	require.True(t, env.svc.isOpenAIAccountModelRuntimeBlocked(env.account(id), "gpt-5.1"))
}

func TestResilienceOpenAILastResortUsesARuntimeBlockedAccount(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, scheduler, lrAccountA)
			breakOpenAIModel(t, env, lrAccountA, 3, time.Now())
			require.Equal(t, lrAccountA, selectOpenAILastResort(t, env).Account.ID)
		})
	}
}

// The block ends first on the account whose breaker opened first: last
// resort prefers it, as it prefers the earliest end of a soft penalty.
func TestResilienceOpenAILastResortOrdersRuntimeBlocksByTheirEnd(t *testing.T) {
	env := newOpenAIRuntimeBlockEnv(t, sharesched.SchedulerSub2API, lrAccountA, lrAccountB)
	now := time.Now()
	breakOpenAIModel(t, env, lrAccountA, 3, now)
	breakOpenAIModel(t, env, lrAccountB, 3, now.Add(-20*time.Second))
	require.Equal(t, lrAccountB, selectOpenAILastResort(t, env).Account.ID)

	tier, until, _ := env.svc.resilienceOpenAIPenaltyOf(env.account(lrAccountA), "gpt-5.1", now)
	require.Equal(t, resilience.TierSoft, tier)
	require.WithinDuration(t, now.Add(openAIModelTransientLongCooldown), until, time.Millisecond)
}

// Tasks 0.4.1 follow-up (design §4.2 exception): a session bound to an
// account whose model is only held back by the in-process transient
// breaker keeps its binding while another account serves; the block ends
// within 10 to 45 s and the session returns to its warm cache.
func TestOpenAIRuntimeBlockedOwnerKeepsTheSessionBinding(t *testing.T) {
	const key = "openai:" + lrSession
	for name, advanced := range map[string]bool{"legacy": false, "advanced": true} {
		t.Run(name, func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, sharesched.SchedulerSub2API, lrAccountA, lrAccountB)
			if advanced {
				env.svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			}
			env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
			breakOpenAIModel(t, env, lrAccountA, 3, time.Now())

			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(cacheableContext(), st)
			gid := shareTestGroupID
			sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, lrAccountB, sel.Account.ID, "the healthy account serves meanwhile")
			admission := ContextWithSelectionProfitGate(ctx, sel)
			require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(admission, &gid, lrSession, sel.Account.ID))
			if sel.ReleaseFunc != nil {
				sel.ReleaseFunc()
			}
			require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "the briefly blocked owner keeps the binding")
		})
	}
}
