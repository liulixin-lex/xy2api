//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Tasks F2 follow-up: when every account of the group is soft-penalised and
// last resort fails only because the new session hits an account's session
// limit, the "no account" answer depends on the request. It must not enter
// the negative cache, which would answer the group's bound sessions with a
// 503 for its lifetime.
func TestLastResortSessionLimitIsNotCachedForTheGroup(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newResilienceGateEnv(t, scheduler,
				func(a *Account, now time.Time) {
					a.Type = AccountTypeOAuth
					a.Extra = map[string]any{"max_sessions": 1}
					softPenalise(a, now)
				},
				func(_ *GatewayService, l *resilienceGateLimits) { l.full[gateTestAccount] = true })
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(cacheableContext(), st)
			gid := shareTestGroupID
			_, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, "sess-new", gateTestModel, nil, "", 0)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.True(t, st.LastResortSessionLimited())

			next := resilience.WithRequestState(cacheableContext(), resilience.NewRequestState())
			require.NoError(t, resilienceNegativeCacheHit(next, &gid, gateTestModel, "", nil), "no group-wide cached 503")
		})
	}
}
