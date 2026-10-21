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

// TestShareCountersFromTheSwitch: a window that starts at the switch never
// counts what was recorded before it, also when it is read by the hour. The
// switch's partial hour is read minute by minute while those buckets are
// kept, and left out otherwise.
func TestShareCountersFromTheSwitch(t *testing.T) {
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewShareSchedulingStore(rdb)
	const groupID = 7
	now := time.Date(2026, 10, 20, 15, 40, 0, 0, time.UTC)
	write := func(at time.Time, n int64) {
		t.Helper()
		require.NoError(t, store.WriteStats(ctx, service.ShareSchedulingStatsBatch{Counters: map[service.ShareSchedulingCounter]int64{
			{Kind: "share", GroupID: groupID, Minute: at.Unix() / 60, Field: "requests"}: n}}))
	}
	read := func(from time.Time, exact bool) int64 {
		t.Helper()
		got, err := store.ReadCounters(ctx, "share", groupID, from, now, exact)
		require.NoError(t, err)
		return got["requests"]
	}

	// Switched at 11:20, four hours twenty minutes ago: its minute buckets
	// are gone, and the 11:00 hour holds 100 requests from before the switch.
	write(time.Date(2026, 10, 20, 11, 5, 0, 0, time.UTC), 100)
	write(time.Date(2026, 10, 20, 11, 40, 0, 0, time.UTC), 1)
	write(time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC), 10)
	write(time.Date(2026, 10, 20, 15, 10, 0, 0, time.UTC), 1000)
	anchor := time.Date(2026, 10, 20, 11, 20, 0, 0, time.UTC)
	require.Equal(t, int64(1010), read(anchor, true), "counting starts at the next whole hour")
	require.Equal(t, int64(1111), read(anchor, false), "a plain window starts at the nearest hour")

	// Switched at 13:33, two hours seven minutes ago: the minutes of 13:33
	// to 14:00 are still kept and replace the 13:00 hour.
	write(time.Date(2026, 10, 20, 13, 10, 0, 0, time.UTC), 200)
	write(time.Date(2026, 10, 20, 13, 45, 0, 0, time.UTC), 20)
	write(time.Date(2026, 10, 20, 14, 30, 0, 0, time.UTC), 30)
	anchor = time.Date(2026, 10, 20, 13, 33, 0, 0, time.UTC)
	require.Equal(t, int64(1050), read(anchor, true))
	require.Equal(t, int64(1030), read(anchor, false), "a plain window rounds 13:33 to 14:00")

	// A switch on the hour has no partial hour.
	require.Equal(t, int64(1260), read(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), true))
}
