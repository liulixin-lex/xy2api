//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// groupSlotCacheStub records the members written and released; the embedded
// ConcurrencyCache is nil, so any other call would panic.
type groupSlotCacheStub struct {
	ConcurrencyCache
	acquired []string
	released []string
}

func (c *groupSlotCacheStub) AcquireAccountSlot(_ context.Context, _ int64, _ int, requestID string) (bool, error) {
	c.acquired = append(c.acquired, requestID)
	return true, nil
}

func (c *groupSlotCacheStub) ReleaseAccountSlot(_ context.Context, _ int64, requestID string) error {
	c.released = append(c.released, requestID)
	return nil
}

// groupSlotReaderCacheStub adds the optional group slot reader.
type groupSlotReaderCacheStub struct {
	ConcurrencyCache
	counts    map[int64]map[int64]int
	err       error
	requested []int64
	ctxErr    error
	deadline  bool
}

func (c *groupSlotReaderCacheStub) GetAccountGroupConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]map[int64]int, error) {
	c.requested = append([]int64(nil), accountIDs...)
	c.ctxErr = ctx.Err()
	_, c.deadline = ctx.Deadline()
	return c.counts, c.err
}

var _ AccountGroupSlotReader = (*groupSlotReaderCacheStub)(nil)

func TestAcquireAccountSlotTagsTheMemberWithTheContextGroup(t *testing.T) {
	cases := []struct {
		name   string
		ctx    context.Context
		suffix string
	}{
		{name: "scheduling group in context", ctx: context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 42}), suffix: "|g42"},
		{name: "no group in context", ctx: context.Background()},
		{name: "group without id", ctx: context.WithValue(context.Background(), ctxkey.Group, &Group{})},
		{name: "nil group", ctx: context.WithValue(context.Background(), ctxkey.Group, (*Group)(nil))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := &groupSlotCacheStub{}
			result, err := NewConcurrencyService(cache).AcquireAccountSlot(tc.ctx, 7, 3)
			require.NoError(t, err)
			require.True(t, result.Acquired)
			require.Len(t, cache.acquired, 1)
			member := cache.acquired[0]
			// The process prefix stays first, so release, trimming and the
			// startup prefix cleanup keep working on suffixed members.
			require.True(t, strings.HasPrefix(member, RequestIDPrefix()+"-"), member)
			if tc.suffix == "" {
				require.NotContains(t, member, slotMemberGroupSeparator)
			} else {
				require.True(t, strings.HasSuffix(member, tc.suffix), member)
			}

			result.ReleaseFunc()
			require.Equal(t, []string{member}, cache.released, "release removes the exact member that was written")
		})
	}
}

func TestWithSlotGroupIDTagsLiveLeases(t *testing.T) {
	groupID := int64(9)
	zero := int64(0)
	require.Equal(t, "r1-a|g9", withSlotGroupID(&groupID, "r1-a"))
	require.Equal(t, "r1-a", withSlotGroupID(nil, "r1-a"))
	require.Equal(t, "r1-a", withSlotGroupID(&zero, "r1-a"))
}

func TestSlotMemberGroupID(t *testing.T) {
	for member, want := range map[string]int64{
		"rabc-1|g10":       10,
		"rabc-z9|g7":       7,
		"rabc-1|g10|g20":   20,
		"rabc-1":           0,
		"rabc-1|g":         0,
		"rabc-1|gx":        0,
		"rabc-1|g-3":       0,
		"rabc-1|g0":        0,
		"live-integration": 0,
		"":                 0,
	} {
		require.Equal(t, want, slotMemberGroupID(member), member)
	}
	groupID := int64(31)
	require.Equal(t, int64(31), slotMemberGroupID(withSlotGroupID(&groupID, generateRequestID())))
}

func TestGetAccountGroupConcurrencyBatchFailsOpen(t *testing.T) {
	t.Run("returns the reader's counts on a detached context", func(t *testing.T) {
		cache := &groupSlotReaderCacheStub{counts: map[int64]map[int64]int{1: {10: 2}}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := NewConcurrencyService(cache).GetAccountGroupConcurrencyBatch(ctx, []int64{1, 2})
		require.Equal(t, map[int64]map[int64]int{1: {10: 2}}, got)
		require.Equal(t, []int64{1, 2}, cache.requested)
		require.NoError(t, cache.ctxErr, "a cancelled request must not cancel the read")
		require.True(t, cache.deadline, "the read has its own timeout")
		require.Equal(t, 3*time.Second, accountGroupConcurrencyReadTimeout)
	})

	t.Run("reader errors yield an empty result", func(t *testing.T) {
		cache := &groupSlotReaderCacheStub{err: errors.New("redis down")}
		got := NewConcurrencyService(cache).GetAccountGroupConcurrencyBatch(context.Background(), []int64{1})
		require.NotNil(t, got)
		require.Empty(t, got)
		require.Zero(t, got[1][10])
	})

	t.Run("caches without the reader yield an empty result", func(t *testing.T) {
		got := NewConcurrencyService(&groupSlotCacheStub{}).GetAccountGroupConcurrencyBatch(context.Background(), []int64{1})
		require.NotNil(t, got)
		require.Empty(t, got)
	})

	t.Run("nil service, nil cache and no ids skip the read", func(t *testing.T) {
		var nilService *ConcurrencyService
		require.Empty(t, nilService.GetAccountGroupConcurrencyBatch(context.Background(), []int64{1}))
		require.Empty(t, (&ConcurrencyService{}).GetAccountGroupConcurrencyBatch(context.Background(), []int64{1}))
		cache := &groupSlotReaderCacheStub{}
		require.Empty(t, NewConcurrencyService(cache).GetAccountGroupConcurrencyBatch(context.Background(), nil))
		require.Nil(t, cache.requested)
	})
}

func TestGroupSlotsUsedSumsOneGroupAcrossAccounts(t *testing.T) {
	counts := map[int64]map[int64]int{1: {10: 1, 20: 2}, 2: {10: 4}}
	current := map[int64]int{1: 3, 2: 4, 3: 2}
	require.Equal(t, 5, groupSlotsUsed(counts, 10, []int64{1, 2, 3}, current))
	require.Equal(t, 2, groupSlotsUsed(counts, 20, []int64{1, 2}, current))
	require.Zero(t, groupSlotsUsed(nil, 10, []int64{1}, current))

	// The total is read first: a slot taken between the two reads is clamped
	// away, so the green number never exceeds the first number.
	require.Equal(t, 1, groupSlotsUsed(counts, 10, []int64{1, 2}, map[int64]int{1: 3}))
	require.Equal(t, 2, groupSlotsWithin(counts, 2, 10, 2))
	require.Zero(t, groupSlotsWithin(counts, 1, 20, 0))
	require.Zero(t, groupSlotsWithin(counts, 1, 20, -1))
}
