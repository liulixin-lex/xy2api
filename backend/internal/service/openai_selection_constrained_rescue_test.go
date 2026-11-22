//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type selectionPressureSlots struct {
	schedulerTestConcurrencyCache
	limits []int
	after  func(int64)
}

func (c *selectionPressureSlots) AcquireAccountSlot(ctx context.Context, id int64, limit int, request string) (bool, error) {
	c.limits = append(c.limits, limit)
	if c.after != nil {
		c.after(id)
	}
	return c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, id, limit, request)
}

// A soft-penalized OAuth account is only available via last resort.
func TestConstrainedSelectionSkipsLastResort(t *testing.T) {
	for _, tc := range []struct {
		name        string
		constrained bool
		pressure    int
	}{
		{"ordinary", false, 1}, {"retry-without-pressure", true, 5}, {"retry-under-pressure", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			account := selectionFixtureAccount(91, 1)
			account.Type, account.Concurrency = AccountTypeOAuth, 5
			until := time.Now().Add(45 * time.Second)
			account.RateLimitResetAt = &until
			account.Extra = map[string]any{codexPressureKey: map[string]any{"limit": tc.pressure}}
			f := newSelectionRuntimeFixture(t, account)
			f.svc.cfg.Gateway.OpenAICodexTicket.AdaptiveConcurrencyAccountIDs = []int64{account.ID}
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, account.ID, leases)
			slots := &selectionPressureSlots{schedulerTestConcurrencyCache: f.loads.schedulerTestConcurrencyCache}
			f.svc.concurrencyService = NewConcurrencyService(slots)
			ctx := selectionReviewContext()
			if tc.constrained {
				ctx = WithOpenAISelectionCandidateConstraint(ctx, 1, OpenAIAttemptModel(&account, "m"), 99)
			}
			r, err := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, true)
			if r != nil && r.ReleaseFunc != nil {
				r.ReleaseFunc()
			}
			t.Logf("RESCUE constrained=%t selected=%t limits=%v releases=%v", tc.constrained, r != nil, slots.limits, f.releases)
			if tc.constrained {
				require.True(t, isNoAccountSelectionError(err))
				require.Nil(t, r)
				require.Empty(t, slots.limits)
				require.Empty(t, f.releases)
				require.Empty(t, leases.held)
				require.Empty(t, leases.calls)
				return
			}
			require.NoError(t, err)
			require.True(t, r.Acquired)
			require.Equal(t, account.ID, r.Account.ID)
			require.Equal(t, []int{tc.pressure}, slots.limits)
			require.Equal(t, []int64{account.ID}, f.releases)
		})
	}
}

func TestConstrainedSelectionRechecksEffectiveLimit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after int
		changed       bool
	}{
		{"steady-pressure", 1, 1, false}, {"tightened", 5, 1, true}, {"relaxed", 1, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, other := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
			account.Type, account.Concurrency = AccountTypeOAuth, 5
			other.Type, other.Concurrency = AccountTypeOAuth, 5
			account.Extra = map[string]any{codexPressureKey: map[string]any{"limit": tc.before}}
			f := newSelectionRuntimeFixture(t, account, other)
			f.svc.cfg.Gateway.OpenAICodexTicket.AdaptiveConcurrencyAccountIDs = []int64{account.ID}
			rows := &selectionLimitRows{selectionRuntimeRepo: f.repo, reads: map[int64]int{}}
			f.svc.accountRepo = rows
			slots := &selectionPressureSlots{schedulerTestConcurrencyCache: f.loads.schedulerTestConcurrencyCache, after: func(id int64) {
				if id == account.ID {
					f.repo.accounts[0].Extra = map[string]any{codexPressureKey: map[string]any{"limit": tc.after}}
				}
			}}
			f.svc.concurrencyService = NewConcurrencyService(slots)
			ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, OpenAIAttemptModel(&account, "m"), 99)
			r, err := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, false)
			require.NoError(t, err)
			require.NotNil(t, r)
			defer r.ReleaseFunc()
			require.Equal(t, 2, rows.reads[account.ID], "pressure uses the existing pre- and post-slot reads")
			if tc.changed {
				require.Equal(t, other.ID, r.Account.ID)
				require.Equal(t, []int{tc.before, other.Concurrency}, slots.limits)
				require.Equal(t, []int64{account.ID}, f.releases)
			} else {
				require.Equal(t, account.ID, r.Account.ID)
				require.Equal(t, []int{tc.before}, slots.limits)
				require.Empty(t, f.releases)
			}
		})
	}
}
