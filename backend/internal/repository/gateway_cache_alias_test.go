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

func TestStickyBindingAliasesConcurrentCompletion(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	testStickyBindingAliases(t, addr)
}

func testStickyBindingAliases(t *testing.T, addr string) {
	t.Helper()
	ctx := context.Background()
	first := redis.NewClient(&redis.Options{Addr: addr})
	second := redis.NewClient(&redis.Options{Addr: addr})
	one, two := &gatewayCache{rdb: first}, &gatewayCache{rdb: second}
	const group = int64(-1023)
	primary, legacy := "openai:v2:"+uuid.NewString(), "openai:"+uuid.NewString()
	avoid := "sticky_change:" + uuid.NewString()
	keys := append(sessionBindingKeys(group, primary), sessionBindingKeys(group, legacy)...)
	t.Cleanup(func() {
		_ = first.Del(ctx, append(keys, sessionKey(resilienceSessionAvoidPrefix, group, avoid), sessionKey(resilienceSessionAvoidWrittenPrefix, group, avoid))...).Err()
		_ = first.Close()
		_ = second.Close()
	})
	// Start with the scalar-only legacy format from before generation fencing.
	require.NoError(t, first.Set(ctx, buildSessionKey(group, legacy), 10, 5*time.Minute).Err())
	var snapshots [2]service.StickySessionBinding
	var errs [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, cache := range []*gatewayCache{one, two} {
		wg.Add(1)
		go func(i int, cache *gatewayCache) {
			defer wg.Done()
			<-start
			snapshots[i], errs[i] = cache.ResolveSessionBinding(ctx, group, primary, legacy, true)
		}(i, cache)
	}
	close(start)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, snapshots[0], snapshots[1], "both instances capture one canonical generation")
	require.EqualValues(t, 10, snapshots[0].AccountID)
	require.Equal(t, legacy, snapshots[0].LegacyKey)
	// The second request keeps the original owner and refreshes during Forward.
	owner, err := two.ClaimSessionAccountID(ctx, group, primary, 10, time.Hour, true)
	require.NoError(t, err)
	require.EqualValues(t, 10, owner)
	require.NoError(t, two.SyncSessionBindingAlias(ctx, group, primary, legacy, 10, 10*time.Minute, false))
	var changed [2]bool
	start = make(chan struct{})
	for i, cache := range []*gatewayCache{one, two} {
		wg.Add(1)
		go func(i int, cache *gatewayCache) {
			defer wg.Done()
			<-start
			changed[i], errs[i] = cache.CommitSessionBinding(ctx, group, primary, snapshots[i], int64(i+1), time.Hour)
		}(i, cache)
	}
	close(start)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.NotEqual(t, changed[0], changed[1])
	current, err := one.GetSessionBinding(ctx, group, primary)
	require.NoError(t, err)
	alias, err := two.GetSessionBinding(ctx, group, legacy)
	require.NoError(t, err)
	require.Equal(t, current, alias, "successful CAS updates both owners and generations")
	require.NotEqual(t, snapshots[0].Generation, current.Generation)
	// A delayed old owner's dual-write or refresh must not hide that success.
	require.NoError(t, two.SyncSessionBindingAlias(ctx, group, primary, legacy, 10, time.Hour, true))
	owner, err = two.ClaimSessionAccountID(ctx, group, primary, 10, time.Hour, true)
	require.NoError(t, err)
	require.Equal(t, current.AccountID, owner)
	alias, err = two.GetSessionBinding(ctx, group, legacy)
	require.NoError(t, err)
	require.Equal(t, current, alias)
	recorded, err := two.RecordSessionBindingFailure(ctx, group, primary, avoid, snapshots[1], current.AccountID, 2*time.Minute)
	require.NoError(t, err)
	require.False(t, recorded)
	deleted, err := two.DeleteSessionBindingAliases(ctx, group, primary, legacy, 10)
	require.NoError(t, err)
	require.False(t, deleted)
	deleted, err = two.DeleteSessionBindingAliases(ctx, group, primary, legacy, current.AccountID)
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = one.ResolveSessionBinding(ctx, group, primary, legacy, true)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound, "deleted aliases cannot resurrect the binding")
	require.NoError(t, one.SetSessionAccountID(ctx, group, legacy, 10, time.Minute))
	_, err = one.ResolveSessionBinding(ctx, group, primary, legacy, true)
	require.NoError(t, err)
	ok, err := two.CommitSessionBinding(ctx, group, primary, snapshots[0], 3, time.Hour)
	require.NoError(t, err)
	require.False(t, ok, "legacy re-adoption fences the earlier generation")
}

func TestStickyBindingAliasesPreserveIdleAndExpireTogether(t *testing.T) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ctx := &gatewayCache{rdb: client}, context.Background()
	const primary, legacy = "openai:v2:expiry", "openai:legacy-expiry"
	require.NoError(t, cache.SetSessionAccountID(ctx, 7, legacy, 10, time.Minute))
	fixture.FastForward(10 * time.Second)
	_, err := cache.ResolveSessionBinding(ctx, 7, primary, legacy, false)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	before, err := cache.ResolveSessionBinding(ctx, 7, primary, legacy, true)
	require.NoError(t, err)
	require.Equal(t, 50*time.Second, client.PTTL(ctx, buildSessionKey(7, primary)).Val(), "adoption does not reset idle time")
	ok, err := cache.CommitSessionBinding(ctx, 7, primary, before, 1, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, cache.SyncSessionBindingAlias(ctx, 7, primary, legacy, 1, time.Hour, true))
	require.Equal(t, 30*time.Second, client.PTTL(ctx, buildSessionKey(7, legacy)).Val(), "alias cannot outlive the authoritative binding")
	fixture.FastForward(31 * time.Second)
	_, err = cache.ResolveSessionBinding(ctx, 7, primary, legacy, true)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
}
