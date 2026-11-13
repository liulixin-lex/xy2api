package repository

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"os"
	"sync"
	"testing"
	"time"
)

func TestStickyBindingGenerationConcurrentCommit(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	one := redis.NewClient(&redis.Options{Addr: addr})
	two := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	const group = int64(-1020)
	key := "openai:v2:generation:" + uuid.NewString()
	t.Cleanup(func() { _ = one.Del(ctx, sessionBindingKeys(group, key)...).Err(); _ = one.Close(); _ = two.Close() })
	first, second := &gatewayCache{rdb: one}, &gatewayCache{rdb: two}
	require.NoError(t, first.SetSessionAccountID(ctx, group, key, 1, time.Hour))
	snapshot, err := first.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	other, err := second.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	require.Equal(t, snapshot, other)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var changed [2]bool
	var errs [2]error
	for i, cache := range []*gatewayCache{first, second} {
		wg.Add(1)
		go func(i int, cache *gatewayCache) {
			defer wg.Done()
			<-start
			changed[i], errs[i] = cache.CommitSessionBinding(ctx, group, key, snapshot, int64(i+2), time.Hour)
		}(i, cache)
	}
	close(start)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.NotEqual(t, changed[0], changed[1], "only one completion may commit the captured generation")
	current, err := first.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	require.NotEqual(t, snapshot.Generation, current.Generation)
	ok, err := second.CommitSessionBinding(ctx, group, key, current, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = first.CommitSessionBinding(ctx, group, key, snapshot, 4, time.Hour)
	require.NoError(t, err)
	require.False(t, ok, "A -> B -> A must reject the old completion")
	beforeDelete, err := first.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	require.NoError(t, first.DeleteSessionAccountID(ctx, group, key))
	_, err = first.ClaimSessionAccountID(ctx, group, key, 1, time.Hour, true)
	require.NoError(t, err)
	ok, err = second.CommitSessionBinding(ctx, group, key, beforeDelete, 4, time.Hour)
	require.NoError(t, err)
	require.False(t, ok, "recreation must invalidate outstanding completions")
}

func TestStickyBindingGenerationExpiresWithBinding(t *testing.T) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	ctx := context.Background()
	const group = int64(7)
	const key = "openai:v2:expiry"
	require.NoError(t, cache.SetSessionAccountID(ctx, group, key, 1, time.Minute))
	old, err := cache.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	require.NoError(t, cache.RefreshSessionTTL(ctx, group, key, time.Hour))
	fixture.FastForward(2 * time.Minute)
	current, err := cache.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	require.Equal(t, old, current)
	fixture.FastForward(time.Hour)
	_, err = cache.GetSessionBinding(ctx, group, key)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	created, err := cache.SetSessionAccountIDIfAbsent(ctx, group, key, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, created)
	ok, err := cache.CommitSessionBinding(ctx, group, key, old, 2, time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
}
