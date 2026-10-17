//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// With the OpenAI advanced scheduler off (the default) the original
// scheduler takes its legacy path. A soft-penalised owner keeps its binding
// there too while the request is served elsewhere (design §4.2), as under
// the advanced scheduler and the Gateway.

func TestResilienceOpenAILegacySchedulerKeepsSoftBinding(t *testing.T) {
	rules := []sharesched.AccountRule{{AccountID: lrAccountA, Tier: 1, Weight: 1}, {AccountID: lrAccountB, Tier: 1, Weight: 1}}
	const key = "openai:" + lrSession

	t.Run("last_resort", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, sharesched.SchedulerSub2API, 5, 1, rules...)
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		sel := selectOpenAILastResort(t, env)
		require.Equal(t, lrAccountB, sel.Account.ID, "the original order is by priority")
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "the soft-penalised owner keeps the binding")
	})

	t.Run("healthy_alternative", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, sharesched.SchedulerSub2API, 5, 1, rules...)
		healthy := env.account(lrAccountB)
		healthy.TempUnschedulableUntil, healthy.TempUnschedulableReason = nil, ""
		healthy.RateLimitResetAt, healthy.OverloadUntil = nil, nil
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)

		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(cacheableContext(), st)
		gid := shareTestGroupID
		sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, lrAccountB, sel.Account.ID)
		require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, sel), &gid, lrSession, sel.Account.ID))
		if sel.ReleaseFunc != nil {
			sel.ReleaseFunc()
		}
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "served elsewhere, the binding stays on the owner")
	})
}
