package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingRejectsInvalidAmounts(t *testing.T) {
	for _, bad := range []float64{-1, -0.0000000001, math.NaN(), math.Inf(1), math.Inf(-1), 1e12} {
		c := &UsageBillingCommand{BalanceCost: bad}
		require.ErrorIs(t, c.Validate(), ErrUsageBillingInvalidAmount)
	}
	c := &UsageBillingCommand{BalanceCost: 0.000078125}
	require.NoError(t, c.Validate())
	c.Normalize()
	require.Equal(t, 0.00007813, c.BalanceCost)
}

func TestConfiguredBillingCannotFallBackToNonAtomicWrites(t *testing.T) {
	_, err := applyUsageBilling(context.Background(), "bill", &UsageLog{}, &postUsageBillingParams{
		Cost: &CostBreakdown{ActualCost: 1}, User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Account: &Account{ID: 3},
	}, &billingDeps{cfg: &config.Config{RunMode: config.RunModeStandard}}, nil)
	require.ErrorIs(t, err, ErrUsageBillingUnavailable)
}

func TestBillingSnapshotContainsNoJoinedCredentials(t *testing.T) {
	u := &UsageLog{ActualCost: 1.25, User: &User{PasswordHash: "do-not-persist"},
		APIKey: &APIKey{Key: "do-not-persist"}, Account: &Account{Credentials: map[string]any{"secret": "do-not-persist"}}}
	snapshot := BillingUsageSnapshot(u)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "do-not-persist")
	require.NotNil(t, u.APIKey, "snapshot must not modify caller-owned objects")
}
