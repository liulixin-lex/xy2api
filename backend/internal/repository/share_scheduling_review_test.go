package repository

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func shareReviewRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	r := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = r.Close() })
	require.NoError(t, r.Ping(context.Background()).Err())
	return r
}
func TestShareReviewWaitOwnershipAndReleaseFence(t *testing.T) {
	r := shareReviewRedis(t)
	c := NewConcurrencyCache(r, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	id := time.Now().UnixNano()
	t.Cleanup(func() {
		keys, _ := r.Keys(ctx, shareWaitKeys(id, "")[0]+"*").Result()
		if len(keys) > 0 {
			_ = r.Del(ctx, keys...).Err()
		}
	})
	ok, err := c.RegisterAccountWait(ctx, id, "one", 1, time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = c.RegisterAccountWait(ctx, id, "one", 1, time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = c.RegisterAccountWait(ctx, id, "two", 1, time.Second)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, c.ReleaseAccountWait(ctx, id, "two", time.Second))
	require.Equal(t, int64(1), r.ZCard(ctx, shareWaitKeys(id, "one")[0]).Val())
	require.NoError(t, c.ReleaseAccountWait(ctx, id, "one", time.Second))
	require.NoError(t, c.ReleaseAccountWait(ctx, id, "one", time.Second))
	ok, err = c.RegisterAccountWait(ctx, id, "one", 1, time.Second)
	require.NoError(t, err)
	require.False(t, ok, "released attempt cannot resurrect")
	ok, err = c.RegisterAccountWait(ctx, id, "three", 1, time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	// Simulate expiry using the Redis server's clock without waiting or changing
	// another process's clock. Registration must reap only expired members.
	require.NoError(t, r.ZAdd(ctx, shareWaitKeys(id, "three")[0], redis.Z{Score: 0, Member: "three"}).Err())
	ok, err = c.RegisterAccountWait(ctx, id, "four", 1, 2*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, c.ReleaseAccountWait(ctx, id, "three", time.Second))
	require.Equal(t, int64(1), r.ZCard(ctx, shareWaitKeys(id, "four")[0]).Val())
}
func TestShareReviewBindingCompareAndSetKeepsConcurrentWinner(t *testing.T) {
	r := shareReviewRedis(t)
	cache := NewGatewayCache(r).(*gatewayCache)
	ctx := context.Background()
	key := uuid.NewString()
	group := int64(-902)
	t.Cleanup(func() { _ = r.Del(ctx, buildSessionKey(group, key)).Err() })
	require.NoError(t, cache.SetSessionAccountID(ctx, group, key, 2, time.Minute))
	owner, err := cache.UpdateSessionAccountIDIfOwner(ctx, group, key, 1, 3, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(2), owner)
	require.LessOrEqual(t, r.PTTL(ctx, buildSessionKey(group, key)).Val(), time.Minute)
	owner, err = cache.UpdateSessionAccountIDIfOwner(ctx, group, key, 2, 3, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(3), owner)
}
func TestShareReviewStatsRetryIsIdempotentAndBucketsAtomic(t *testing.T) {
	r := shareReviewRedis(t)
	store := NewShareSchedulingStore(r)
	ctx := context.Background()
	gid := time.Now().UnixNano()
	minute := time.Now().Unix() / 60
	batch := service.ShareSchedulingStatsBatch{ID: uuid.NewString(), Counters: map[service.ShareSchedulingCounter]int64{
		{Kind: "share", GroupID: gid, Minute: minute, Field: "requests"}: 7,
		{Kind: "share", GroupID: gid, Minute: minute, Field: "served:1"}: 7,
	}, Decisions: map[int64][][]byte{gid: {[]byte(`{"served":1}`)}}}
	t.Cleanup(func() {
		for _, pattern := range []string{shareCounterKey("share", gid, minute), shareHourCounterKey("share", gid, minute/60), shareDecisionKey(gid) + "*", "xy2:stats:batch:*:*:" + strconv.FormatInt(gid, 10) + ":*"} {
			keys, _ := r.Keys(ctx, pattern).Result()
			if len(keys) > 0 {
				_ = r.Del(ctx, keys...).Err()
			}
		}
	})
	require.NoError(t, store.WriteStats(ctx, batch))
	require.NoError(t, store.WriteStats(ctx, batch))
	require.Equal(t, "7", r.HGet(ctx, shareCounterKey("share", gid, minute), "requests").Val())
	require.Equal(t, "7", r.HGet(ctx, shareHourCounterKey("share", gid, minute/60), "requests").Val())
	require.Equal(t, int64(1), r.LLen(ctx, shareDecisionKey(gid)).Val())
	// A wrong-type hourly bucket must not increment the minute copy.
	batch.ID = uuid.NewString()
	require.NoError(t, r.Del(ctx, shareHourCounterKey("share", gid, minute/60)).Err())
	require.NoError(t, r.Set(ctx, shareHourCounterKey("share", gid, minute/60), "corrupt", time.Minute).Err())
	err := store.WriteStats(ctx, batch)
	require.Error(t, err)
	require.False(t, errors.Is(err, redis.Nil))
	require.Equal(t, "7", r.HGet(ctx, shareCounterKey("share", gid, minute), "requests").Val())
	require.NoError(t, r.Del(ctx, shareHourCounterKey("share", gid, minute/60)).Err())
	require.NoError(t, store.WriteStats(ctx, batch))
	require.Equal(t, "14", r.HGet(ctx, shareCounterKey("share", gid, minute), "requests").Val())
	require.Equal(t, int64(2), r.LLen(ctx, shareDecisionKey(gid)).Val(), "a sample committed before the failed bucket is not duplicated on retry")
	require.NoError(t, r.HSet(ctx, shareCounterKey("share", gid, minute), "requests", "corrupt").Err())
	_, err = store.ReadCounters(ctx, "share", gid, time.Unix(minute*60, 0), time.Unix(minute*60, 0))
	require.Error(t, err, "invalid numbers must not appear as a complete zero-traffic report")
}

func TestShareReviewWaitLimitAcrossInstances(t *testing.T) {
	r := shareReviewRedis(t)
	two := redis.NewClient(r.Options())
	defer two.Close()
	a := NewConcurrencyCache(r, 15, 900).(*concurrencyCache)
	b := NewConcurrencyCache(two, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	id := time.Now().UnixNano()
	attempts := make(chan string, 60)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c := a
			if i%2 == 1 {
				c = b
			}
			attempt := uuid.NewString()
			ok, err := c.RegisterAccountWait(ctx, id, attempt, 3, time.Second)
			if err != nil {
				attempts <- "error"
				return
			}
			if ok {
				attempts <- attempt
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(attempts)
	var owners []string
	for attempt := range attempts {
		require.NotEqual(t, "error", attempt)
		owners = append(owners, attempt)
	}
	require.Len(t, owners, 3)
	require.Equal(t, int64(3), r.ZCard(ctx, shareWaitKeys(id, "")[0]).Val())
	for _, attempt := range owners {
		require.NoError(t, b.ReleaseAccountWait(ctx, id, attempt, time.Second))
	}
	require.Zero(t, r.ZCard(ctx, shareWaitKeys(id, "")[0]).Val())
	keys, _ := r.Keys(ctx, shareWaitKeys(id, "")[0]+"*").Result()
	if len(keys) > 0 {
		require.NoError(t, r.Del(ctx, keys...).Err())
	}
}
