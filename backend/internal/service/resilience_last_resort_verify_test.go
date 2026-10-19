//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Review R09: last resort plans with a group list cached for 2 s. An
// account the administrator disabled after that list was read must not be
// dispatched, on either scheduler.

func TestGatewayLastResortSkipsAccountsDisabledAfterTheGroupListWasCached(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, scheduler, 1, 1)
			_, err := env.gateway.resilience.groupAccountsForLastResort(context.Background(), env.gateway.accountRepo, shareTestGroupID)
			require.NoError(t, err)
			env.repo.accountsByID[lrAccountA].Schedulable = false
			env.repo.accountsByID[lrAccountB].Schedulable = false

			ctx := resilience.WithRequestState(cacheableContext(), resilience.NewRequestState())
			gid := shareTestGroupID
			result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, lrSession, "claude-sonnet-4-5", nil, "", 0)
			if err == nil {
				require.Nil(t, result.Account, "a disabled account was dispatched as the last resort")
			}
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
		})
	}
}

func TestGatewayLastResortServesTheStillSchedulableAccount(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, scheduler, 1, 5)
			_, err := env.gateway.resilience.groupAccountsForLastResort(context.Background(), env.gateway.accountRepo, shareTestGroupID)
			require.NoError(t, err)
			env.repo.accountsByID[lrAccountA].Schedulable = false
			result, _ := selectGatewayLastResort(t, env)
			require.Equal(t, lrAccountB, result.Account.ID)
		})
	}
}

func TestOpenAILastResortSkipsAccountsDisabledAfterTheGroupListWasCached(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newOpenAILastResortEnv(t, scheduler, 1, 1)
			_, err := env.svc.resilience.groupAccountsForLastResort(context.Background(), env.svc.accountRepo, shareTestGroupID)
			require.NoError(t, err)
			env.account(lrAccountA).Status = StatusDisabled
			env.account(lrAccountB).Schedulable = false

			ctx := resilience.WithRequestState(cacheableContext(), resilience.NewRequestState())
			gid := shareTestGroupID
			sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			if err == nil && sel != nil {
				require.Nil(t, sel.Account, "a disabled account was dispatched as the last resort")
			}
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
		})
	}
}
