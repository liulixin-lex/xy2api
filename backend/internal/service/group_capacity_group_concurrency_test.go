//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// groupCapacityGroupSlotCacheStub adds the optional group slot reader to the
// capacity cache stub.
type groupCapacityGroupSlotCacheStub struct {
	groupCapacityConcurrencyCacheStub
	groupCounts    map[int64]map[int64]int
	groupRequested []int64
}

func (s *groupCapacityGroupSlotCacheStub) GetAccountGroupConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]map[int64]int, error) {
	s.groupRequested = append([]int64(nil), accountIDs...)
	return s.groupCounts, nil
}

type groupCapacitySequentialAccountRepoStub struct {
	AccountRepository
	accounts map[int64][]Account
}

func (s *groupCapacitySequentialAccountRepoStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	return append([]Account(nil), s.accounts[groupID]...), nil
}

// Account 1 serves groups 10 and 20 and holds 3 slots: one taken through
// group 10, two through group 20. Both groups show 3 in use; the green number
// is 1 and 2.
func TestGetAllGroupCapacityCountsGroupSlotsOnSharedAccounts(t *testing.T) {
	newCache := func() *groupCapacityGroupSlotCacheStub {
		return &groupCapacityGroupSlotCacheStub{
			groupCapacityConcurrencyCacheStub: groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 3}},
			groupCounts:                       map[int64]map[int64]int{1: {10: 1, 20: 2}},
		}
	}
	want := []GroupCapacitySummary{
		{GroupID: 10, ConcurrencyUsed: 3, ConcurrencyMax: 5, ConcurrencyGroupUsed: 1},
		{GroupID: 20, ConcurrencyUsed: 3, ConcurrencyMax: 5, ConcurrencyGroupUsed: 2},
	}

	t.Run("batch path", func(t *testing.T) {
		accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{
			{GroupID: 10, AccountID: 1, Concurrency: 5},
			{GroupID: 20, AccountID: 1, Concurrency: 5},
		}}
		cache := newCache()
		svc := NewGroupCapacityService(accountRepo, &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}, NewConcurrencyService(cache), nil, nil)

		results, err := svc.GetAllGroupCapacity(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, results)
		require.Equal(t, []int64{1}, cache.groupRequested, "one read over the same accounts as the total")
	})

	t.Run("sequential fallback", func(t *testing.T) {
		accountRepo := &groupCapacitySequentialAccountRepoStub{accounts: map[int64][]Account{
			10: {{ID: 1, Concurrency: 5}},
			20: {{ID: 1, Concurrency: 5}},
		}}
		svc := NewGroupCapacityService(accountRepo, &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}, NewConcurrencyService(newCache()), nil, nil)

		results, err := svc.GetAllGroupCapacity(context.Background())
		require.NoError(t, err)
		require.Equal(t, want, results)
	})

	t.Run("a slot taken between the two reads is clamped to the total", func(t *testing.T) {
		accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{
			{GroupID: 10, AccountID: 1, Concurrency: 5},
			{GroupID: 20, AccountID: 1, Concurrency: 5},
		}}
		cache := newCache()
		cache.counts = map[int64]int{1: 1}
		svc := NewGroupCapacityService(accountRepo, &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}, NewConcurrencyService(cache), nil, nil)

		results, err := svc.GetAllGroupCapacity(context.Background())
		require.NoError(t, err)
		require.Equal(t, []GroupCapacitySummary{
			{GroupID: 10, ConcurrencyUsed: 1, ConcurrencyMax: 5, ConcurrencyGroupUsed: 1},
			{GroupID: 20, ConcurrencyUsed: 1, ConcurrencyMax: 5, ConcurrencyGroupUsed: 1},
		}, results)
	})
}
