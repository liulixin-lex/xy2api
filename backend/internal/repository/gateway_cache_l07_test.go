//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheL07State(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewGatewayCache(rdb).(*gatewayCache)
	store := NewResilienceStore(rdb).(*resilienceStore)
	clock := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return clock }
	ctx := context.Background()

	// Nothing stored yet: zero state, no error.
	state, err := cache.L07ReadBusy(ctx, 31, "sess", 1)
	require.NoError(t, err)
	require.Equal(t, service.L07BusyState{}, state)

	// The failure count is the resilience store's own hash, read in the same
	// round trip; another account's count is not reported.
	_, err = store.IncrSessionFailure(ctx, 31, "sess", 1, 10*time.Minute)
	require.NoError(t, err)
	_, err = store.IncrSessionFailure(ctx, 31, "sess", 2, 10*time.Minute)
	require.NoError(t, err)
	require.NoError(t, cache.L07WriteBusy(ctx, 31, "sess", 1, 1234, 2, 120*time.Second))
	state, err = cache.L07ReadBusy(ctx, 31, "sess", 1)
	require.NoError(t, err)
	require.Equal(t, service.L07BusyState{Owner: 1, FirstBusyMs: 1234, BusyN: 2, FailCount: 1, FailEndMs: clock.Add(10 * time.Minute).UnixMilli()}, state)
	require.Equal(t, 120*time.Second, server.TTL("l07:31:sess"))

	// An owner hit refreshes the binding and ends the segment in one pipeline.
	require.NoError(t, cache.SetSessionAccountID(ctx, 31, "sess", 1, time.Minute))
	cleared, err := cache.L07RefreshSessionTTLClear(ctx, 31, "sess", time.Hour)
	require.NoError(t, err)
	require.True(t, cleared)
	require.Equal(t, time.Hour, server.TTL(buildSessionKey(31, "sess")))
	require.False(t, server.Exists("l07:31:sess"))
	cleared, err = cache.L07RefreshSessionTTLClear(ctx, 31, "sess", time.Hour)
	require.NoError(t, err)
	require.False(t, cleared)

	require.NoError(t, cache.L07WriteBusy(ctx, 31, "s2", 3, 1, 1, time.Minute))
	cleared, err = cache.L07Clear(ctx, 31, "s2")
	require.NoError(t, err)
	require.True(t, cleared)

	// A malformed field fails the read: the caller abandons the keep.
	server.HSet("l07:31:bad", "busy_n", "x")
	_, err = cache.L07ReadBusy(ctx, 31, "bad", 1)
	require.Error(t, err)
	server.Close()
	_, err = cache.L07ReadBusy(ctx, 31, "sess", 1)
	require.Error(t, err)
}
