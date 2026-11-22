//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrdinaryBorrowChecksPrivacyOnFreshRow(t *testing.T) {
	o, b, c := selectionFixtureAccount(10, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(4, 3)
	fresh := map[string]any{"privacy_mode": PrivacyModeTrainingOff}
	o.Extra = fresh
	c.Extra = fresh
	f := newSelectionRuntimeFixture(t, o, b, c)
	f.repo.accounts[1].Extra = fresh
	cache := &snapshotHydrationCache{snapshot: []*Account{&o, &b, &c}, accounts: map[int64]*Account{10: &o, 2: &b, 4: &c}}
	f.svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, nil, guardianAffinityGroupRepo{group: &Group{ID: 7, Name: "private", Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RequirePrivacySet: true}}, nil)
	f.cache.sessionBindings["openai:bound"] = 10
	f.loads.acquireResults = map[int64]bool{10: false}
	// Force the usual owner's queue-free borrow. No retry constraint is installed.
	ctx := WithOpenAISelectionNoWait(selectionReviewContext())
	r, err := selectOrdinaryNewSessionForTest(t, f, ctx, "bound", nil, true)
	require.NoError(t, err)
	require.NotNil(t, r)
	defer r.ReleaseFunc()
	require.True(t, r.StickyCapacitySpillover)
	require.True(t, r.Account.IsPrivacySet())
	t.Logf("PRIVACY ordinary selected=%d acquired=%v", r.Account.ID, f.attempts)
	require.EqualValues(t, 2, r.Account.ID)
}
