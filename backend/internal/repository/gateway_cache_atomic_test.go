package repository

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAICacheStickyAtomic(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	first := redis.NewClient(&redis.Options{Addr: addr})
	second := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	ctx := context.Background()
	require.NoError(t, first.Ping(ctx).Err())
	key := "openai:v2:atomic-test:" + uuid.NewString()
	group := int64(-1010)
	t.Cleanup(func() { _ = first.Del(ctx, buildSessionKey(group, key)).Err() })
	one, ok := NewGatewayCache(first).(service.OpenAIStickyAtomicCache)
	require.True(t, ok)
	two, ok := NewGatewayCache(second).(service.OpenAIStickyAtomicCache)
	require.True(t, ok)
	start := make(chan struct{})
	type outcome struct {
		owner int64
		err   error
	}
	results := make(chan outcome, 100)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cache := one
			if i%2 == 1 {
				cache = two
			}
			owner, err := cache.ClaimSessionAccountID(ctx, group, key, int64(i%2+1), time.Hour, false)
			results <- outcome{owner, err}
		}(i)
	}
	close(start)
	wg.Wait()
	owner, err := first.Get(ctx, buildSessionKey(group, key)).Int64()
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		r := <-results
		require.NoError(t, r.err)
		require.Equal(t, owner, r.owner)
	}
	ttl, err := first.PTTL(ctx, buildSessionKey(group, key)).Result()
	require.NoError(t, err)
	require.LessOrEqual(t, ttl, 30*time.Second, "unadmitted reservations expire quickly")
	other := int64(3) - owner
	actual, err := two.ClaimSessionAccountID(ctx, group, key, other, time.Hour, true)
	require.NoError(t, err)
	require.Equal(t, owner, actual)
	ttl, err = first.PTTL(ctx, buildSessionKey(group, key)).Result()
	require.NoError(t, err)
	require.LessOrEqual(t, ttl, 30*time.Second, "another account must not promote the reservation")
	actual, err = one.ClaimSessionAccountID(ctx, group, key, owner, time.Hour, true)
	require.NoError(t, err)
	require.Equal(t, owner, actual)
	ttl, err = first.PTTL(ctx, buildSessionKey(group, key)).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 59*time.Minute)
	require.NoError(t, two.DeleteSessionAccountIDIfOwner(ctx, group, key, other))
	loaded, err := first.Get(ctx, buildSessionKey(group, key)).Int64()
	require.NoError(t, err)
	require.Equal(t, owner, loaded)
	require.NoError(t, one.DeleteSessionAccountIDIfOwner(ctx, group, key, owner))
	actual, err = two.ClaimSessionAccountID(ctx, group, key, other, time.Hour, true)
	require.NoError(t, err)
	require.Equal(t, other, actual)
	require.NoError(t, one.DeleteSessionAccountIDIfOwner(ctx, group, key, owner))
	loaded, err = first.Get(ctx, buildSessionKey(group, key)).Int64()
	require.NoError(t, err)
	require.Equal(t, other, loaded, "stale cleanup cannot delete a later owner")
}

func TestOpenAICacheUnadmittedReservationExpires(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	var fixture *miniredis.Miniredis
	if addr == "" {
		fixture = miniredis.RunT(t)
		addr = fixture.Addr()
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewGatewayCache(client).(service.OpenAIStickyAtomicCache)
	require.True(t, ok)
	ctx := context.Background()
	key := "openai:v2:expiry-test:" + uuid.NewString()
	group := int64(-1010)
	t.Cleanup(func() { _ = client.Del(ctx, buildSessionKey(group, key)).Err() })
	owner, err := cache.ClaimSessionAccountID(ctx, group, key, 1, 100*time.Millisecond, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, owner)
	if fixture != nil {
		fixture.FastForward(time.Second)
	}
	require.Eventually(t, func() bool { return client.Exists(ctx, buildSessionKey(group, key)).Val() == 0 }, 2*time.Second, 10*time.Millisecond)
	owner, err = cache.ClaimSessionAccountID(ctx, group, key, 2, time.Hour, true)
	require.NoError(t, err)
	require.EqualValues(t, 2, owner)
}
