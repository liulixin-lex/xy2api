//go:build unit

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Startup cleanup of a new process must keep the slots of another process
// that is still alive on the same Redis (blue-green cutover), and sweep them
// once that process is gone.
func TestStartupCleanupKeepsSlotsOfLiveProcesses(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := NewConcurrencyCache(rdb, 15, 60).(*concurrencyCache)
	require.NoError(t, rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err())

	const accountID = int64(41)
	slots := accountSlotKey(accountID)
	now := time.Now().Unix()
	require.NoError(t, rdb.ZAdd(ctx, slots,
		redis.Z{Score: float64(now + 900), Member: "rold-7"},
		redis.Z{Score: float64(now + 900), Member: "rdead-3"},
	).Err())
	require.NoError(t, rdb.Set(ctx, accountWaitKey(accountID), 2, time.Minute).Err())
	require.NoError(t, rdb.ZAdd(ctx, accountActiveIndexKey, redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(accountID, 10)}).Err())
	// The old process still serves and holds its lease.
	require.NoError(t, rdb.Set(ctx, processLeaseKey("rold"), now, processLeaseTTL).Err())

	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rnew"))
	members, err := rdb.ZRange(ctx, slots, 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"rold-7"}, members, "the live process keeps its slot, the dead one's is gone")
	wait, err := rdb.Get(ctx, accountWaitKey(accountID)).Int()
	require.NoError(t, err)
	require.Equal(t, 2, wait, "wait counts are shared while another process lives")
	own, err := rdb.Exists(ctx, processLeaseKey("rnew")).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), own, "the new process holds a lease")

	// While the old process lives, the periodic sweep keeps its slot.
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	members, _ = rdb.ZRange(ctx, slots, 0, -1).Result()
	require.Equal(t, []string{"rold-7"}, members)

	// Its lease is missing once (a Redis hiccup, or it just exited): the
	// slot stays until a second sweep misses it too.
	require.NoError(t, rdb.Del(ctx, processLeaseKey("rold")).Err())
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	members, _ = rdb.ZRange(ctx, slots, 0, -1).Result()
	require.Equal(t, []string{"rold-7"}, members, "one missed lease does not clear a process")
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	members, _ = rdb.ZRange(ctx, slots, 0, -1).Result()
	require.Empty(t, members, "two missed leases in a row clear the leftover")
	wait, err = rdb.Get(ctx, accountWaitKey(accountID)).Int()
	require.NoError(t, err)
	require.Equal(t, 2, wait, "the running sweep leaves the shared wait count to its TTL")
}

func TestLeaseSweepForgetsAProcessThatCameBack(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := NewConcurrencyCache(rdb, 15, 60).(*concurrencyCache)
	require.NoError(t, rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
	const accountID = int64(43)
	now := time.Now().Unix()
	require.NoError(t, rdb.ZAdd(ctx, accountSlotKey(accountID), redis.Z{Score: float64(now + 900), Member: "rpeer-1"}).Err())
	require.NoError(t, rdb.ZAdd(ctx, accountActiveIndexKey, redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(accountID, 10)}).Err())
	require.NoError(t, rdb.Set(ctx, processLeaseKey("rpeer"), now, processLeaseTTL).Err())
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rself"))

	// Missed once, then refreshed again: the count of misses restarts.
	require.NoError(t, rdb.Del(ctx, processLeaseKey("rpeer")).Err())
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	require.NoError(t, rdb.Set(ctx, processLeaseKey("rpeer"), now, processLeaseTTL).Err())
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	require.NoError(t, rdb.Del(ctx, processLeaseKey("rpeer")).Err())
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	members, err := rdb.ZRange(ctx, accountSlotKey(accountID), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"rpeer-1"}, members, "the misses are counted in a row")
}

func TestStartupCleanupAloneDropsOtherPrefixesAndWaits(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := NewConcurrencyCache(rdb, 15, 60).(*concurrencyCache)
	require.NoError(t, rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
	const userID = int64(42)
	now := time.Now().Unix()
	require.NoError(t, rdb.ZAdd(ctx, userSlotKey(userID), redis.Z{Score: float64(now + 900), Member: "rcrashed-1"}).Err())
	require.NoError(t, rdb.Set(ctx, waitQueueKey(userID), 3, time.Minute).Err())
	require.NoError(t, rdb.ZAdd(ctx, userActiveIndexKey, redis.Z{Score: float64(now + 60), Member: strconv.FormatInt(userID, 10)}).Err())

	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rnew"))
	n, err := rdb.Exists(ctx, userSlotKey(userID), waitQueueKey(userID)).Result()
	require.NoError(t, err)
	require.Zero(t, n, "a crashed process without a lease is cleaned at once, as before")
}
