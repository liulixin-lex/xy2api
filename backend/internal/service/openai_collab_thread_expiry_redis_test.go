//go:build unit

package service_test

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
