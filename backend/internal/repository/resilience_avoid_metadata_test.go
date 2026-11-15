package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSessionAvoidWriteMetadataSurvivesTTLChanges(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	testSessionAvoidWriteMetadata(t, addr)
}

func testSessionAvoidWriteMetadata(t *testing.T, addr string) {
	t.Helper()
	ctx := context.Background()
	one := redis.NewClient(&redis.Options{Addr: addr})
	two := redis.NewClient(&redis.Options{Addr: addr})
	session := "synthetic-write-time:" + uuid.NewString()
	const group = int64(-1031)
	key := sessionKey(resilienceSessionAvoidPrefix, group, session)
	metadata := sessionKey(resilienceSessionAvoidWrittenPrefix, group, session)
	t.Cleanup(func() { _ = one.Del(ctx, key, metadata).Err(); _ = one.Close(); _ = two.Close() })
	now := time.Now().Truncate(time.Millisecond)
	first := &resilienceStore{rdb: one, now: func() time.Time { return now }}
	second := &resilienceStore{rdb: two, now: func() time.Time { return now }}
	require.NoError(t, first.AddSessionAvoid(ctx, group, session, 1, 600*time.Second))
	initial := now
	for _, tc := range []struct{ advance, ttl, deadline, reset time.Duration }{
		{100 * time.Second, 60 * time.Second, 600 * time.Second, 0},
		{200 * time.Second, 1200 * time.Second, 1400 * time.Second, 0},
		{300 * time.Second, 60 * time.Second, 360 * time.Second, 250 * time.Second},
	} {
		now = initial.Add(tc.advance)
		var reset time.Time
		if tc.reset > 0 {
			reset = initial.Add(tc.reset)
		}
		require.NoError(t, second.AddSessionAvoid(ctx, group, session, 1, tc.ttl, reset))
		records, err := first.SessionAvoidWrittenAt(ctx, group, session)
		require.NoError(t, err)
		require.Equal(t, now, records[1], "write time tracks the new fact independently of the retained deadline")
		require.Equal(t, float64(initial.Add(tc.deadline).UnixMilli()), one.ZScore(ctx, key, "1").Val())
		require.InDelta(t, one.PTTL(ctx, key).Val().Milliseconds(), two.PTTL(ctx, metadata).Val().Milliseconds(), 5)
	}
	// A scalar member written by an overlap instance has no metadata.
	require.NoError(t, one.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(time.Minute).UnixMilli()), Member: "2"}).Err())
	records, err := second.SessionAvoidWrittenAt(ctx, group, session)
	require.NoError(t, err)
	require.Contains(t, records, int64(2))
	require.True(t, records[2].IsZero())
}
