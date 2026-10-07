package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type integrityCacheStub struct {
	BillingCache
	err           error
	invalidations int
}

func (c *integrityCacheStub) InvalidateUserBalance(context.Context, int64) error {
	c.invalidations++
	return c.err
}

type integrityRecoveryRepoStub struct {
	UsageBillingRepository
	applied   bool
	completed int
}

func (r *integrityRecoveryRepoStub) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	cmd.Usage.ID = 42
	return &UsageBillingApplyResult{Applied: r.applied}, nil
}
func (r *integrityRecoveryRepoStub) ClaimBillingRecovery(context.Context, int) ([]UsageBillingCommand, error) {
	return nil, nil
}
func (r *integrityRecoveryRepoStub) BillingRecoveryHealth(context.Context) (int64, int64, float64, error) {
	return 0, 0, 0, nil
}
func (r *integrityRecoveryRepoStub) CompleteBillingCacheInvalidation(context.Context, *UsageBillingCommand) error {
	r.completed++
	return nil
}

func TestDurableBillingCacheCheckpoint(t *testing.T) {
	for _, applied := range []bool{true, false} {
		for _, failed := range []bool{true, false} {
			cache := &integrityCacheStub{}
			if failed {
				cache.err = errors.New("cache unavailable")
			}
			r := &integrityRecoveryRepoStub{applied: applied}
			params := &postUsageBillingParams{Cost: &CostBreakdown{ActualCost: 1, TotalCost: 1}, User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Account: &Account{ID: 3}}
			log := &UsageLog{RequestID: "event", UserID: 1, APIKeyID: 2, AccountID: 3, ActualCost: 1, TotalCost: 1}
			got, err := applyUsageBilling(context.Background(), "event", log, params,
				&billingDeps{billingCacheService: &BillingCacheService{cache: cache}, deferredService: &DeferredService{}}, r)
			require.NoError(t, err, "a cache failure must not undo committed money")
			require.Equal(t, applied, got)
			require.Equal(t, 1, cache.invalidations, "duplicates must also repair the cache without replaying a delta")
			if failed {
				require.Zero(t, r.completed, "failed cache invalidation must remain recoverable")
			} else {
				require.Equal(t, 1, r.completed, "normal requests must not build an unbounded recovery backlog")
			}
		}
	}
}
