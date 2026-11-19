package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestFirstOutputFollowCASPreservesOwnerAvoid(t *testing.T) {
	ctx := context.Background()
	db := miniredis.RunT(t)
	first := redis.NewClient(&redis.Options{Addr: db.Addr()})
	second := redis.NewClient(&redis.Options{Addr: db.Addr()})
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	one, two := &gatewayCache{rdb: first}, &gatewayCache{rdb: second}
	const group, key, scope = int64(7), "openai:session", "first_output:session"
	require.NoError(t, one.SetSessionAccountID(ctx, group, key, 10, time.Hour))
	snapshot, err := two.GetSessionBinding(ctx, group, key)
	require.NoError(t, err)
	written, err := one.RecordSessionBindingFailure(ctx, group, key, scope, snapshot, 10, 600*time.Second)
	require.NoError(t, err)
	require.True(t, written)
	avoidKey := sessionKey(resilienceSessionAvoidPrefix, group, scope)
	expiry, err := second.ZScore(ctx, avoidKey, "10").Result()
	require.NoError(t, err)
	moved, err := two.CommitSessionBinding(ctx, group, key, snapshot, 11, time.Hour, "sticky_change:session")
	require.NoError(t, err)
	require.True(t, moved)
	moved, err = one.CommitSessionBinding(ctx, group, key, snapshot, 12, time.Hour, "sticky_change:session")
	require.NoError(t, err)
	require.False(t, moved)
	written, err = one.RecordSessionBindingFailure(ctx, group, key, scope, snapshot, 10, 600*time.Second)
	require.NoError(t, err)
	require.False(t, written)
	require.Equal(t, expiry, second.ZScore(ctx, avoidKey, "10").Val())
	for _, other := range []string{"session", "sticky_change:session"} {
		avoided, err := NewResilienceStore(second).SessionAvoid(ctx, group, other)
		require.NoError(t, err)
		require.Empty(t, avoided)
	}
}
