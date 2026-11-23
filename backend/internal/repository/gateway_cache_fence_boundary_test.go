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

type collabFenceDelayedRedisBinding struct {
	*gatewayCache
	fixture *miniredis.Miniredis
}

func (c *collabFenceDelayedRedisBinding) CreateResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	// miniredis TTLs use explicit time advancement; align them with elapsed wall time.
	c.fixture.FastForward(service.CollabBindingPublicationLifetime + 100*time.Millisecond)
	return c.gatewayCache.CreateResponseBinding(ctx, group, key, id, ttl)
}

func TestCollabFenceRedisExpiryDuringBoundedWrite(t *testing.T) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &collabFenceDelayedRedisBinding{gatewayCache: &gatewayCache{rdb: client}, fixture: fixture}
	store := service.NewOpenAIWSStateStore(cache)
	peer := service.NewOpenAIWSStateStore(cache)
	start := time.Now()
	persist := store.(service.CollabResponseIdentityStore).PrepareCollabResponseAccount(3, "resp_boundary", 7, time.Hour)
	require.NoError(t, peer.DeleteResponseAccount(context.Background(), 3, "resp_boundary"))
	time.Sleep(service.CollabBindingPublicationLifetime - 100*time.Millisecond)
	callStart := time.Since(start)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, persist(ctx))
	got, err := service.NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 3, "resp_boundary")
	require.NoError(t, err)
	t.Logf("start=%s finish=%s context_error=%v fresh_peer_account=%d", callStart, time.Since(start), ctx.Err(), got)
	require.Zero(t, got, "Redis must not republish a binding deleted after this callback was prepared")
}

// The Redis clock advances while the call is queued, after the caller's age
// check. Expiring all metadata must still leave the old publication inadmissible.
type collabExpiredRedisWrite struct {
	*gatewayCache
	fixture *miniredis.Miniredis
}

func (c *collabExpiredRedisWrite) CreateResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) (bool, error) {
	c.fixture.FastForward(2 * time.Hour)
	c.fixture.SetTime(time.Now().Add(2 * time.Hour))
	return c.gatewayCache.CreateResponseBinding(ctx, group, key, id, ttl)
}
func TestCollabBindingExpiryIsCheckedInsideRedis(t *testing.T) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &collabExpiredRedisWrite{gatewayCache: &gatewayCache{rdb: client}, fixture: fixture}
	first := service.NewOpenAIWSStateStore(cache)
	peer := service.NewOpenAIWSStateStore(cache)
	persist := first.(service.CollabResponseIdentityStore).PrepareCollabResponseAccount(0, "resp_atomic_expiry", 7, time.Hour)
	require.NoError(t, peer.DeleteResponseAccount(context.Background(), 0, "resp_atomic_expiry"))
	require.NoError(t, persist(context.Background()))
	got, err := service.NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_atomic_expiry")
	require.NoError(t, err)
	require.Zero(t, got)
}

func TestCollabProductionDeferredBindingVisibility(t *testing.T) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	// The production constructor is also the cache injected by server wiring.
	cache := NewGatewayCache(client)
	require.Implements(t, (*service.CollabResponseBindingCache)(nil), cache)
	first := service.NewOpenAIWSStateStore(cache)
	peer := service.NewOpenAIWSStateStore(cache)
	publisher := first.(service.CollabResponseIdentityStore)
	ctx := context.Background()
	older := publisher.PrepareCollabResponseAccount(0, "resp_visible", 7, time.Hour)
	owner := publisher.PrepareCollabHTTPResponseOwner(0, "resp_visible", 3, 5, time.Hour)
	local, _ := first.GetResponseAccount(ctx, 0, "resp_visible")
	require.EqualValues(t, 7, local)
	remote, _ := peer.GetResponseAccount(ctx, 0, "resp_visible")
	require.Zero(t, remote)
	require.NoError(t, older(ctx))
	require.NoError(t, owner(ctx))
	remote, _ = peer.GetResponseAccount(ctx, 0, "resp_visible")
	require.EqualValues(t, 7, remote)
	user, key, found, err := peer.GetHTTPResponseOwner(ctx, 0, "resp_visible")
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 3, user)
	require.EqualValues(t, 5, key)
	newer := publisher.PrepareCollabResponseAccount(0, "resp_visible", 9, time.Hour)
	require.NoError(t, newer(ctx))
	require.NoError(t, older(ctx))
	remote, _ = service.NewOpenAIWSStateStore(cache).GetResponseAccount(ctx, 0, "resp_visible")
	// Separate preparations without a request publisher cannot replace a live owner.
	require.EqualValues(t, 7, remote)
	require.NoError(t, first.BindResponseAccount(ctx, 0, "resp_visible", 9, time.Hour))
	remote, _ = service.NewOpenAIWSStateStore(cache).GetResponseAccount(ctx, 0, "resp_visible")
	require.EqualValues(t, 9, remote)
}
