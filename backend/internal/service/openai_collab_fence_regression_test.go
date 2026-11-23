//go:build unit

package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type collabFenceLateBindingCache struct {
	collabBindingTraceCache
	delay time.Duration
}

func (c *collabFenceLateBindingCache) CreateResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(c.delay):
	}
	return c.collabBindingTraceCache.CreateResponseBinding(ctx, group, key, id, ttl)
}

func TestCollabFenceBindingExpiryDuringBoundedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := &collabFenceLateBindingCache{delay: 200 * time.Millisecond}
		store := NewOpenAIWSStateStore(cache)
		peer := NewOpenAIWSStateStore(cache)
		start := time.Now()
		persist := store.(CollabResponseIdentityStore).PrepareCollabResponseAccount(0, "resp_expiry", 7, time.Hour)
		time.Sleep(time.Millisecond)
		require.NoError(t, peer.DeleteResponseAccount(context.Background(), 0, "resp_expiry"))
		time.Sleep(CollabBindingPublicationLifetime - 100*time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		require.NoError(t, persist(ctx))
		got, err := NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_expiry")
		require.NoError(t, err)
		t.Logf("callback_started_at=59.901s completed_at=%s bounded_context_error=%v fresh_peer_account=%d", time.Since(start), ctx.Err(), got)
		require.Zero(t, got, "an old publication must not resurrect a newer deletion while the shared write is in flight")
	})
}

func TestCollabFenceNewerDeferredBindingWins(t *testing.T) {
	cache := &collabBindingTraceCache{}
	store := NewOpenAIWSStateStore(cache)
	publisher := store.(CollabResponseIdentityStore)
	older := publisher.PrepareCollabResponseAccount(0, "resp_two", 7, time.Hour, "same_request")
	require.NoError(t, older(context.Background()))
	newer := publisher.PrepareCollabResponseAccount(0, "resp_two", 9, time.Hour, "same_request")
	require.NoError(t, newer(context.Background()))
	local, _ := store.GetResponseAccount(context.Background(), 0, "resp_two")
	peer, _ := NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_two")
	t.Logf("local_latest=%d shared_latest=%d", local, peer)
	require.EqualValues(t, 9, peer, "the shared binding must publish the newer generation")
}
