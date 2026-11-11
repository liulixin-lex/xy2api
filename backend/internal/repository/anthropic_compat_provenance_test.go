package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newCompatProvenanceTestCache(t *testing.T) (*gatewayCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	server.SetTime(time.Now())
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewGatewayCache(client).(*gatewayCache)
	require.True(t, ok)
	return cache, server
}

func TestAnthropicCompatProvenanceDuplicateKeysAreIdempotentAtCapacity(t *testing.T) {
	cache, _ := newCompatProvenanceTestCache(t)
	ctx := context.Background()
	keys := []string{anthropicCompatProvenanceIndex, "same", "same"}
	for range 2 {
		require.NoError(t, registerAnthropicCompatProvenance.Run(ctx, cache.rdb, keys, "owner", 60000, 1).Err())
	}
	require.EqualValues(t, 1, cache.rdb.ZCard(ctx, anthropicCompatProvenanceIndex).Val())
	require.Equal(t, "owner", cache.rdb.Get(ctx, "same").Val())
}

func TestAnthropicCompatProvenanceIndexOutlivesLongestRecord(t *testing.T) {
	cache, server := newCompatProvenanceTestCache(t)
	ctx := context.Background()
	require.NoError(t, cache.Register(ctx, []string{"long"}, []byte("owner"), time.Hour))
	require.NoError(t, cache.Register(ctx, []string{"short"}, []byte("owner"), time.Second))
	recordTTL := cache.rdb.PTTL(ctx, "long").Val()
	indexTTL := cache.rdb.PTTL(ctx, anthropicCompatProvenanceIndex).Val()
	require.GreaterOrEqual(t, indexTTL, recordTTL-time.Millisecond)
	server.FastForward(2 * time.Second)
	require.Equal(t, "owner", cache.rdb.Get(ctx, "long").Val())
	require.True(t, server.Exists(anthropicCompatProvenanceIndex))
}

func TestAnthropicCompatProvenanceRejectionDoesNotMutateIndex(t *testing.T) {
	for _, failure := range []string{"conflict", "wrong_type", "capacity", "sub_ms"} {
		t.Run(failure, func(t *testing.T) {
			cache, _ := newCompatProvenanceTestCache(t)
			ctx := context.Background()
			require.NoError(t, cache.rdb.ZAdd(ctx, anthropicCompatProvenanceIndex, redis.Z{Score: 1, Member: "expired"}).Err())
			require.NoError(t, cache.Register(ctx, []string{"existing"}, []byte("owner"), time.Minute))
			// A stale index member must also survive a rejected transaction.
			require.NoError(t, cache.rdb.ZAdd(ctx, anthropicCompatProvenanceIndex, redis.Z{Score: 1, Member: "expired"}).Err())
			before := cache.rdb.ZRangeWithScores(ctx, anthropicCompatProvenanceIndex, 0, -1).Val()
			var err error
			switch failure {
			case "conflict":
				err = cache.Register(ctx, []string{"new", "existing"}, []byte("other"), time.Minute)
			case "wrong_type":
				require.NoError(t, cache.rdb.LPush(ctx, "invalid", "value").Err())
				err = cache.Register(ctx, []string{"new", "invalid"}, []byte("owner"), time.Minute)
			case "capacity":
				err = registerAnthropicCompatProvenance.Run(ctx, cache.rdb, []string{anthropicCompatProvenanceIndex, "new"}, "owner", 60000, 1).Err()
			case "sub_ms":
				err = cache.Register(ctx, []string{"new"}, []byte("owner"), time.Nanosecond)
			}
			require.Error(t, err)
			require.Equal(t, before, cache.rdb.ZRangeWithScores(ctx, anthropicCompatProvenanceIndex, 0, -1).Val())
			require.Zero(t, cache.rdb.Exists(ctx, "new").Val())
			require.Equal(t, "owner", cache.rdb.Get(ctx, "existing").Val())
		})
	}
}

func TestAnthropicCompatProvenanceRegistrationErrorsAndConcurrentRetry(t *testing.T) {
	cache, server := newCompatProvenanceTestCache(t)
	ctx := context.Background()
	for _, keys := range [][]string{{""}, {anthropicCompatProvenanceIndex}, {"valid", anthropicCompatProvenanceIndex}} {
		require.ErrorIs(t, cache.Register(ctx, keys, []byte("owner"), time.Minute), service.AnthropicCompatRegistrationInvalid)
	}
	require.Empty(t, server.Keys())
	keys := []string{"one", "two", "one"}
	errs := make([]error, 16)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			owner := []byte{byte('a' + i%2)}
			errs[i] = cache.Register(ctx, keys, owner, time.Minute)
		})
	}
	wg.Wait()
	values, err := cache.Lookup(ctx, keys)
	require.NoError(t, err)
	for i, err := range errs {
		if values[0][0] == byte('a'+i%2) {
			require.NoError(t, err, "same constraints are idempotent under contention")
		} else {
			require.ErrorIs(t, err, service.AnthropicCompatRegistrationConflict)
		}
	}
	for _, value := range values {
		require.Equal(t, values[0], value)
	}
	require.EqualValues(t, 2, cache.rdb.ZCard(ctx, anthropicCompatProvenanceIndex).Val())
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, cache.Register(cancelled, []string{"cancelled"}, values[0], time.Minute), context.Canceled)
	require.False(t, server.Exists("cancelled"))
	server.FastForward(2 * time.Minute)
	values, err = cache.Lookup(ctx, keys)
	require.NoError(t, err)
	for _, value := range values {
		require.Empty(t, value)
	}
}
