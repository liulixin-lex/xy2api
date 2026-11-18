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
	require.NotContains(t, fields, "w:1")
	require.NotContains(t, fields, "m:1")
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
	other := NewResilienceStore(rdb).(*resilienceStore)
	clearAt := func(id int64, start time.Time) {
		require.NoError(t, other.ClearSessionFailuresBefore(ctx, 7, "sess", id, start))
	}
	add(1)
	add(2)
	failure = at.Add(-time.Second)
	add(1) // an out-of-order failure cannot lower the existing watermark
	for _, d := range []time.Duration{-time.Second, 0, 4 * time.Millisecond, service.SessionFailureClockSkewAllowance, service.SessionFailureClockSkewAllowance + 999*time.Microsecond} {
		clearAt(1, at.Add(d))
		require.Equal(t, "2", rdb.HGet(ctx, key, "1").Val())
	}
	clearAt(1, at.Add(service.SessionFailureClockSkewAllowance+time.Millisecond))
	require.ElementsMatch(t, []string{"2", "e:2", "f:2", "w:2", "m:2"}, rdb.HKeys(ctx, key).Val())
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

// A same-account retry is usually dispatched within the two-second clock margin.
// If the same instance wrote the watermark, the retry breaks the failure streak.
func TestSessionFailureRetrySuccessUsesWriterClock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local bool
		delay time.Duration
		want  int64
	}{
		{"same_instance_500ms", true, 500 * time.Millisecond, 1},
		{"another_instance_500ms", false, 500 * time.Millisecond, 2},
		{"same_instance_600ms", true, 600 * time.Millisecond, 1},
		{"another_instance_600ms", false, 600 * time.Millisecond, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			db := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = db.Close() })
			writer := NewResilienceStore(db).(*resilienceStore)
			reader := writer
			if !tc.local {
				reader = NewResilienceStore(db).(*resilienceStore)
			}
			ctx := context.Background()
			at := time.Now()
			clock := at
			writer.now = func() time.Time { return clock }
			n, err := writer.IncrSessionFailure(ctx, 7, "s", 1, 10*time.Minute)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			require.NoError(t, reader.ClearSessionFailuresBefore(ctx, 7, "s", 1, at.Add(tc.delay)))
			clock = at.Add(5 * time.Minute)
			n, err = writer.IncrSessionFailure(ctx, 7, "s", 1, 10*time.Minute)
			require.NoError(t, err)
			require.Equal(t, tc.want, n)
		})
	}
}

// Exercise the same clock interleavings with deterministic Redis and, when
// installed, an isolated redis-server executing the production Lua scripts.
func testSessionFailureClocks(t *testing.T, check func(*testing.T, *redis.Client)) {
	t.Helper()
	t.Run("miniredis", func(t *testing.T) {
		mr := miniredis.RunT(t)
		db := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = db.Close() })
		check(t, db)
	})
	t.Run("redis-server", func(t *testing.T) { check(t, newLocalRedisClient(t)) })
}

func TestSessionFailureEarlierLocalWriteAfterForeignFailure(t *testing.T) {
	testSessionFailureClocks(t, func(t *testing.T, db *redis.Client) {
		for _, tc := range []struct {
			name      string
			foreignAt time.Duration
			seedLocal bool
		}{
			{"foreign_equal", 8 * time.Millisecond, true},
			{"foreign_earlier", 7 * time.Millisecond, true},
			{"foreign_later", 9 * time.Millisecond, true},
			{"foreign_first", 7 * time.Millisecond, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				local := NewResilienceStore(db).(*resilienceStore)
				foreign := NewResilienceStore(db).(*resilienceStore)
				base := time.Now()
				key := sessionKey(resilienceSessionFailurePrefix, 7, tc.name)
				fail := func(store *resilienceStore, at time.Time) {
					t.Helper()
					store.now = func() time.Time { return at }
					_, err := store.IncrSessionFailure(ctx, 7, tc.name, 1, time.Minute)
					require.NoError(t, err)
				}
				if tc.seedLocal {
					fail(local, base.Add(8*time.Millisecond))
				}
				// The local clock leads by 8 ms: a failure at t+1 was sampled
				// before dispatch at t+2 but reaches Redis after the foreign failure.
				queued := base.Add(9 * time.Millisecond)
				dispatch := base.Add(10 * time.Millisecond)
				fail(foreign, base.Add(tc.foreignAt))
				fail(local, queued)
				want := 2
				if tc.seedLocal {
					want++
				}
				require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, tc.name, 1, dispatch))
				count, err := db.HGet(ctx, key, "1").Int()
				require.NoError(t, err)
				require.Equal(t, want, count)
				// Even a newly advanced wall watermark must keep this window mixed.
				fail(local, dispatch.Add(time.Millisecond))
				require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, tc.name, 1, dispatch.Add(500*time.Millisecond)))
				require.False(t, db.HExists(ctx, key, "w:1").Val())
				count, err = db.HGet(ctx, key, "1").Int()
				require.NoError(t, err)
				require.Equal(t, want+1, count)
				require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, tc.name, 1, dispatch.Add(3*time.Second)))
				require.False(t, db.HExists(ctx, key, "1").Val())
				// After clearing, a new local-only window can use monotonic order again.
				fail(local, dispatch.Add(4*time.Second))
				require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, tc.name, 1, dispatch.Add(4500*time.Millisecond)))
				require.Zero(t, db.Exists(ctx, key).Val())
			})
		}
	})
}

func TestSessionFailureStrictLocalClearAcrossWallClockStep(t *testing.T) {
	testSessionFailureClocks(t, func(t *testing.T, db *redis.Client) {
		ctx := context.Background()
		local := NewResilienceStore(db).(*resilienceStore)
		send := time.Now()
		wallFailure := send.Add(-4 * time.Millisecond)
		// The wall clock stepped back 8 ms. Relative to process start the send
		// is at 100 ms and the failure is at 104 ms. Simulate both readings
		// without changing the host clock.
		local.monotonicMillis = func(at time.Time) int64 {
			if at == wallFailure {
				return 104
			}
			return 100 + at.Sub(send).Milliseconds()
		}
		fail := func(at time.Time) {
			local.now = func() time.Time { return at }
			_, err := local.IncrSessionFailure(ctx, 7, "step", 1, time.Minute)
			require.NoError(t, err)
		}
		fail(wallFailure)
		fail(send.Add(-time.Millisecond)) // earlier local sample arrives last
		key := sessionKey(resilienceSessionFailurePrefix, 7, "step")
		for _, offset := range []time.Duration{0, 4 * time.Millisecond} {
			require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, "step", 1, send.Add(offset)))
			require.Equal(t, "2", db.HGet(ctx, key, "1").Val())
		}
		require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, "step", 1, send.Add(5*time.Millisecond)))
		require.Zero(t, db.Exists(ctx, key).Val())
	})
}

func TestSessionFailureMissingMonotonicClockUsesWallMargin(t *testing.T) {
	mr := miniredis.RunT(t)
	db := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	local := NewResilienceStore(db).(*resilienceStore)
	at := time.Now()
	for _, tc := range []string{"old_window", "untimed_failure", "untimed_dispatch", "old_window_without_writer"} {
		t.Run(tc, func(t *testing.T) {
			key := sessionKey(resilienceSessionFailurePrefix, 7, tc)
			fail := func(stamp time.Time) {
				local.now = func() time.Time { return stamp }
				_, err := local.IncrSessionFailure(ctx, 7, tc, 1, time.Minute)
				require.NoError(t, err)
			}
			fail(at)
			send := at.Add(500 * time.Millisecond)
			switch tc {
			case "old_window":
				require.NoError(t, db.HDel(ctx, key, "m:1").Err())
				fail(at.Add(time.Millisecond))
			case "old_window_without_writer":
				require.NoError(t, db.HDel(ctx, key, "w:1", "m:1").Err())
				fail(at.Add(time.Millisecond))
			case "untimed_failure":
				fail(at.Round(0))
				fail(at.Add(time.Millisecond))
			case "untimed_dispatch":
				send = send.Round(0)
			}
			require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, tc, 1, send))
			require.True(t, db.HExists(ctx, key, "1").Val())
			require.NoError(t, local.ClearSessionFailuresBefore(ctx, 7, tc, 1, at.Add(3*time.Second).Round(0)))
			require.False(t, db.HExists(ctx, key, "1").Val())
		})
	}
}
