//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Review R06: the session failure count of one account must expire on its
// own; failures of another account of the same session used to refresh the
// whole hash and keep an old count alive.
func TestSessionFailureWindowIsPerAccount(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewResilienceStore(rdb).(*resilienceStore)
	clock := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return clock }
	ctx := context.Background()
	window := time.Second

	n, err := store.IncrSessionFailure(ctx, 7, "sess", 1, window) // A at 0 s
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	clock = clock.Add(650 * time.Millisecond)
	_, err = store.IncrSessionFailure(ctx, 7, "sess", 2, window) // B at 0.65 s
	require.NoError(t, err)
	clock = clock.Add(650 * time.Millisecond)
	n, err = store.IncrSessionFailure(ctx, 7, "sess", 1, window) // A at 1.3 s
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "A's first failure is outside its window")

	failures, err := store.SessionFailures(ctx, 7, "sess")
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{1: 1, 2: 1}, failures)

	// Within the window the account's own failures still add up, and each
	// one keeps its window open.
	clock = clock.Add(600 * time.Millisecond)
	n, err = store.IncrSessionFailure(ctx, 7, "sess", 1, window)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	clock = clock.Add(900 * time.Millisecond)
	failures, err = store.SessionFailures(ctx, 7, "sess")
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{1: 2}, failures, "B's window ended")

	require.NoError(t, store.ClearSessionFailure(ctx, 7, "sess", 1))
	failures, err = store.SessionFailures(ctx, 7, "sess")
	require.NoError(t, err)
	require.Empty(t, failures)
	fields, err := rdb.HKeys(ctx, resilienceSessionFailurePrefix+"7:sess").Result()
	require.NoError(t, err)
	require.NotContains(t, fields, "f:1")
	require.NotContains(t, fields, "e:1", "clearing removes the window end too")
}

func TestClearSessionFailuresBeforeDispatch(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewResilienceStore(rdb).(*resilienceStore)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	failure := at
	store.now = func() time.Time { return failure }
	key := sessionKey(resilienceSessionFailurePrefix, 7, "sess")
	add := func(id int64) {
		_, err := store.IncrSessionFailure(ctx, 7, "sess", id, time.Minute)
		require.NoError(t, err)
	}
	clearAt := func(id int64, start time.Time) {
		require.NoError(t, store.ClearSessionFailuresBefore(ctx, 7, "sess", id, start))
	}
	add(1)
	add(2)
	failure = at.Add(-time.Second)
	add(1) // an out-of-order failure cannot lower the existing watermark
	for _, d := range []time.Duration{-time.Second, 0, 999 * time.Microsecond} {
		clearAt(1, at.Add(d))
		require.Equal(t, "2", rdb.HGet(ctx, key, "1").Val())
	}
	clearAt(1, at.Add(time.Millisecond))
	require.ElementsMatch(t, []string{"2", "e:2", "f:2"}, rdb.HKeys(ctx, key).Val())
	require.NoError(t, rdb.HDel(ctx, key, "f:2").Err()) // existing live window from an older instance
	clearAt(2, at.Add(time.Second))
	require.Equal(t, "1", rdb.HGet(ctx, key, "2").Val())
	failure = at.Add(time.Minute)
	counts, err := store.SessionFailures(ctx, 7, "sess")
	require.NoError(t, err)
	require.Empty(t, counts)
	server.FastForward(time.Minute)
	require.False(t, server.Exists(key))
}
