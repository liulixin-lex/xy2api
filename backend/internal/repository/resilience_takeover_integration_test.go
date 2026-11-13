//go:build integration

package repository

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// FINAL §12: each failed target is shared between instances for two minutes.
func TestTakeoverFailureSharedForTwoMinutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := redis.NewClient(integrationRedis.Options())
	second := redis.NewClient(first.Options())
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	one, two := NewResilienceStore(first), NewResilienceStore(second)
	const group = int64(7)
	session := "synthetic-takeover-failure:" + uuid.NewString()
	t.Cleanup(func() { _ = first.Del(ctx, sessionKey(resilienceSessionAvoidPrefix, group, session)).Err() })
	require.NoError(t, one.AddSessionAvoid(ctx, group, session, 101, 2*time.Minute))
	avoid, err := two.SessionAvoid(ctx, group, session)
	require.NoError(t, err)
	require.Contains(t, avoid, int64(101))
	key := sessionKey(resilienceSessionAvoidPrefix, group, session)
	ttl, err := second.PTTL(ctx, key).Result()
	require.NoError(t, err)
	require.InDelta(t, (2 * time.Minute).Milliseconds(), ttl.Milliseconds(), 1000)
	time.Sleep(2*time.Minute + 100*time.Millisecond)
	avoid, err = two.SessionAvoid(ctx, group, session)
	require.NoError(t, err)
	require.Empty(t, avoid)
	require.Zero(t, second.Exists(ctx, key).Val())
}
