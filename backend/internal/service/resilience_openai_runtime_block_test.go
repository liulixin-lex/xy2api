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
	rules := make([]sharesched.AccountRule, 0, len(ids))
	for _, id := range ids {
		rules = append(rules, sharesched.AccountRule{AccountID: id, Tier: 0, Weight: 1})
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
