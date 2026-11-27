//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCompensationIntentRecoveryChargesFrozenAmountOnce(t *testing.T) {
	repo, cmd := newBillingIntentFixture(t)
	calls := 0
	cmd.ResolveCompensation = func(c *service.UsageBillingCommand) {
		calls++
		c.BalanceCost = .75
		c.APIKeyQuotaCost = .75
		c.APIKeyRateLimitCost = .75
		c.Usage.ActualCost = .75
		c.Usage.MeteringAdjustment = json.RawMessage(`{"v":1,"customer_gross_cost":1.25,"customer_credit":0.5,"adjustments":[{"kind":"openai_voluntary_compensation","amount":0.5}]}`)
	}
	cmd.Normalize()
	frozen, err := repo.prepareBillingIntent(context.Background(), cmd)
	require.NoError(t, err)
	assertBillingIntentMoney(t, cmd, 100, 0)
	require.Nil(t, frozen.ResolveCompensation)
	restarted := &usageBillingRepository{db: integrationDB}
	for i := 0; i < 2; i++ {
		result, e := restarted.Apply(context.Background(), frozen)
		require.NoError(t, e)
		require.Equal(t, i == 0, result.Applied)
	}
	result, err := repo.Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Equal(t, 1, calls)
	assertBillingIntentMoney(t, cmd, 99.25, 1)
	var used, accountUsed float64
	require.NoError(t, integrationDB.QueryRow("SELECT quota_used FROM api_keys WHERE id=$1", cmd.APIKeyID).Scan(&used))
	require.Equal(t, .75, used)
	require.NoError(t, integrationDB.QueryRow("SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1", cmd.AccountID).Scan(&accountUsed))
	require.Equal(t, 1.25, accountUsed)
	require.Equal(t, .75, cmd.Usage.ActualCost)
}
