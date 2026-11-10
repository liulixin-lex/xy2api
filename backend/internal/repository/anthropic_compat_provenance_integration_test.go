//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatProvenanceRedisAtomicConflictCapacityAndIdleTTL(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache, ok := NewGatewayCache(rdb).(*gatewayCache)
	require.True(t, ok)
	owner := []byte(`{"account":71,"profile":"synthetic"}`)
	other := []byte(`{"account":72,"profile":"synthetic"}`)
	require.NoError(t, cache.Register(ctx, []string{"existing"}, owner, time.Minute))
	require.ErrorContains(t, cache.Register(ctx, []string{"new", "existing"}, other, time.Minute), "conflict")
	values, err := cache.Lookup(ctx, []string{"existing", "new"})
	require.NoError(t, err)
	require.Equal(t, owner, values[0])
	require.Empty(t, values[1])
	// The production script's capacity is an argument. Exercise a small ceiling
	// without allocating one million unrelated entries in the test Redis.
	err = registerAnthropicCompatProvenance.Run(ctx, rdb, []string{anthropicCompatProvenanceIndex, "capacity-a", "capacity-b"}, string(owner), 60000, 2).Err()
	require.ErrorContains(t, err, "capacity")
	values, err = cache.Lookup(ctx, []string{"capacity-a", "capacity-b"})
	require.NoError(t, err)
	require.Empty(t, values[0])
	require.Empty(t, values[1])
	require.NoError(t, cache.Register(ctx, []string{"idle-old-root", "idle-current-root"}, owner, time.Second))
	time.Sleep(600 * time.Millisecond)
	// A turn without new output still registers the read/current history keys.
	require.NoError(t, cache.Register(ctx, []string{"idle-old-root", "idle-current-root"}, owner, 2*time.Second))
	time.Sleep(600 * time.Millisecond)
	values, err = cache.Lookup(ctx, []string{"idle-old-root", "idle-current-root"})
	require.NoError(t, err)
	require.Equal(t, owner, values[0])
	require.Equal(t, owner, values[1])
	for _, key := range []string{"idle-old-root", "idle-current-root"} {
		ttl, e := rdb.PTTL(ctx, key).Result()
		require.NoError(t, e)
		require.Greater(t, ttl, time.Second)
	}
	require.NoError(t, cache.Register(ctx, []string{"truly-idle"}, owner, 30*time.Millisecond))
	require.Eventually(t, func() bool {
		out, e := cache.Lookup(ctx, []string{"truly-idle"})
		return e == nil && len(out) == 1 && len(out[0]) == 0
	}, 3*time.Second, 20*time.Millisecond)
	// Expired index members no longer consume capacity.
	require.NoError(t, registerAnthropicCompatProvenance.Run(ctx, rdb, []string{anthropicCompatProvenanceIndex, "after-expiry"}, string(owner), 60000, 4).Err())
}

func TestAnthropicCompatProvenanceRedisConcurrentWritersAndCancellation(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache, ok := NewGatewayCache(rdb).(*gatewayCache)
	require.True(t, ok)
	keys := []string{"one", "two", "three"}
	values := [][]byte{[]byte(`{"owner":1}`), []byte(`{"owner":2}`)}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { errs[i] = cache.Register(ctx, keys, values[i], time.Minute) })
	}
	wg.Wait()
	require.NotEqual(t, errs[0] == nil, errs[1] == nil, "exactly one source may win")
	winner := 0
	if errs[0] != nil {
		winner = 1
	}
	out, err := cache.Lookup(ctx, keys)
	require.NoError(t, err)
	for _, value := range out {
		require.Equal(t, values[winner], value)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, cache.Register(cancelled, []string{"cancelled"}, values[0], time.Minute))
	out, err = cache.Lookup(ctx, []string{"cancelled"})
	require.NoError(t, err)
	require.Empty(t, out[0])
}
