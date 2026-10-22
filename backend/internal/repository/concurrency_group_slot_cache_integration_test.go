//go:build integration

package repository

import (
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func (s *ConcurrencyCacheSuite) groupSlots(accountIDs ...int64) map[int64]map[int64]int {
	s.T().Helper()
	got, err := s.rawCache.GetAccountGroupConcurrencyBatch(s.ctx, accountIDs)
	require.NoError(s.T(), err)
	return got
}

func (s *ConcurrencyCacheSuite) TestAccountGroupSlots_CountsSuffixedSlotsAndLiveLeases() {
	accountID := int64(9201)
	for _, member := range []string{"rp-1|g10", "rp-2|g10", "rp-3|g20", "rp-4"} {
		ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 10, member)
		require.NoError(s.T(), err)
		require.True(s.T(), ok)
	}
	liveCache, ok := s.cache.(service.LiveConcurrencyCache)
	require.True(s.T(), ok)
	ok, err := liveCache.AcquireLiveLease(s.ctx, accountID, 10, 9202, 10, 9203, "rp-5|g20", false)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	require.Equal(s.T(), map[int64]map[int64]int{accountID: {10: 2, 20: 2}}, s.groupSlots(accountID, 9299))
	cur, err := s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 5, cur, "the unsuffixed slot counts in the total only")
}

func (s *ConcurrencyCacheSuite) TestAccountGroupSlots_ExpiredMembersAreNotCountedNorTrimmed() {
	accountID := int64(9211)
	slotKey := accountSlotKey(accountID)
	liveKey := liveAccountSlotKey(accountID)
	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	slotCutoff := float64(now - int64(testSlotTTL.Seconds()))
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, slotKey,
		redis.Z{Score: float64(now), Member: "rp-1|g10"},
		redis.Z{Score: slotCutoff, Member: "rp-2|g10"},
		redis.Z{Score: slotCutoff - 60, Member: "rp-3|g10"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, liveKey,
		redis.Z{Score: float64(now - liveLeaseTTLSeconds - 1), Member: "rp-4|g10"},
	).Err())

	require.Equal(s.T(), map[int64]map[int64]int{accountID: {10: 1}}, s.groupSlots(accountID))
	slots, err := s.rdb.ZCard(s.ctx, slotKey).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(3), slots, "the read is read-only")
	live, err := s.rdb.ZCard(s.ctx, liveKey).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), live)
}

func (s *ConcurrencyCacheSuite) TestAccountGroupSlots_ReleaseClearsTheCount() {
	accountID := int64(9221)
	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 2, "rp-1|g10")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	require.Equal(s.T(), map[int64]map[int64]int{accountID: {10: 1}}, s.groupSlots(accountID))

	require.NoError(s.T(), s.cache.ReleaseAccountSlot(s.ctx, accountID, "rp-1|g10"))
	require.Empty(s.T(), s.groupSlots(accountID))
}

func (s *ConcurrencyCacheSuite) TestAccountGroupSlots_DeadProcessCleanupRemovesSuffixedMembers() {
	require.NoError(s.T(), s.rdb.Set(s.ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
	accountID := int64(9231)
	slotKey := accountSlotKey(accountID)
	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, slotKey,
		redis.Z{Score: float64(now), Member: "oldproc-1|g10"},
		redis.Z{Score: float64(now), Member: "oldproc-2"},
		redis.Z{Score: float64(now), Member: "activeproc-1|g10"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, slotKey, time.Minute).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, accountActiveIndexKey, redis.Z{Score: float64(now + 60), Member: "9231"}).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "activeproc-"))

	members, err := s.rdb.ZRange(s.ctx, slotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"activeproc-1|g10"}, members, "the process prefix still decides which members survive")
	require.Equal(s.T(), map[int64]map[int64]int{accountID: {10: 1}}, s.groupSlots(accountID))
}
