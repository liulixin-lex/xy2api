//go:build unit

package service_test

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCollabThreadExpiryRedis(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	operations := &collabRedisOperations{}
	rdb.AddHook(operations)
	service.RunCollabProvisionalExpiry(t, repository.NewGatewayCache(rdb), repository.NewResilienceStore(rdb), repository.NewSchedTrialLeaseCache(rdb), func() { require.NoError(t, rdb.FlushDB(context.Background()).Err()) }, srv.FastForward, operations.count.Load)
}

func TestCollabThreadTemporalRedis(t *testing.T) {
	var stores [2]service.CollabTemporalStore
	for i := range stores {
		srv := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: -1})
		t.Cleanup(func() { _ = rdb.Close() })
		operations := &collabRedisOperations{}
		rdb.AddHook(operations)
		stores[i] = service.CollabTemporalStore{Cache: repository.NewGatewayCache(rdb), Store: repository.NewResilienceStore(rdb), Leases: repository.NewSchedTrialLeaseCache(rdb), Reset: func() { require.NoError(t, rdb.FlushDB(context.Background()).Err()) }, Advance: srv.FastForward, Operations: operations.count.Load}
	}
	service.RunCollabTemporalOracle(t, stores)
}
