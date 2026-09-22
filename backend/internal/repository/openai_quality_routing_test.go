package repository

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIQualityRedisTransactions(t *testing.T) {
	addr := os.Getenv("QUALITY_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	require.NoError(t, rdb.Ping(ctx).Err())
	cache := &gatewayCache{rdb: rdb}
	secondClient := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = secondClient.Close() })
	instances := []*gatewayCache{cache, {rdb: secondClient}}
	scope := fmt.Sprintf("quality-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		rdb.Del(ctx, qualityRoutingPrefix+scope, buildSessionKey(17, scope+"-sticky"), buildSessionKey(17, scope+"-legacy"))
	})
	require.NoError(t, cache.SetSessionAccountID(ctx, 17, scope+"-sticky", 7, time.Hour))
	require.NoError(t, cache.SetSessionAccountID(ctx, 17, scope+"-legacy", 8, time.Hour))
	st, ids, err := cache.ReadOpenAIQualityRouting(ctx, scope, 17, []string{scope + "-sticky", scope + "-legacy"})
	require.NoError(t, err)
	require.Nil(t, st)
	require.Equal(t, []int64{7, 8}, ids)
	now := time.Now().UnixMilli()
	change := service.OpenAIQualityChange{Kind: "evidence", AccountID: 7, Provider: "digest", Now: now, Until: now + 300000, Expires: now + 3600000, Rotation: "stable-rotation"}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		instance := instances[i%len(instances)]
		go func() {
			defer wg.Done()
			_, err := instance.UpdateOpenAIQualityRouting(ctx, scope, change, time.Hour)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	st, _, err = cache.ReadOpenAIQualityRouting(ctx, scope, 17, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), st.Generation)
	require.Len(t, st.Avoid, 1)
	bind := service.OpenAIQualityChange{Kind: "bind", Generation: 1, AccountID: 8, Now: now, Expires: now + 3600000}
	st, err = cache.UpdateOpenAIQualityRouting(ctx, scope, bind, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(8), st.Binding)
	bind.AccountID = 9
	st, err = cache.UpdateOpenAIQualityRouting(ctx, scope, bind, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(8), st.Binding)
	st, err = cache.UpdateOpenAIQualityRouting(ctx, scope, change, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(8), st.Binding)
	change.Generation = 1
	change.AccountID = 8
	change.Rotation = "second"
	st, err = cache.UpdateOpenAIQualityRouting(ctx, scope, change, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(2), st.Generation)
	require.Zero(t, st.Binding)
	st, err = cache.UpdateOpenAIQualityRouting(ctx, scope, bind, time.Hour)
	require.NoError(t, err)
	require.Zero(t, st.Binding)
	other, _, err := cache.ReadOpenAIQualityRouting(ctx, scope+"-other", 17, nil)
	require.NoError(t, err)
	require.Nil(t, other)
	remaining, err := rdb.PTTL(ctx, qualityRoutingPrefix+scope).Result()
	require.NoError(t, err)
	require.Greater(t, remaining, 59*time.Minute)
	t.Logf("redis=%s duplicate_evidence=32 generation=%d stale_binding=%d scopes_isolated=true", addr, st.Generation, st.Binding)
}
