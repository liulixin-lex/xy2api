//go:build unit

package service_test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type collabRedisOperations struct {
	count     atomic.Int64
	mu        sync.Mutex
	remaining int
	after     func()
}

func (h *collabRedisOperations) arm(n int, after func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remaining, h.after = n, after
}

func (h *collabRedisOperations) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return next(ctx, network, address)
	}
}
func (h *collabRedisOperations) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if !redis.HasErrorPrefix(err, "NOSCRIPT") {
			h.count.Add(1)
			h.mu.Lock()
			h.remaining--
			var after func()
			if h.remaining == 0 {
				after, h.after = h.after, nil
			}
			h.mu.Unlock()
			if after != nil {
				after()
			}
		}
		return err
	}
}
func (h *collabRedisOperations) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.count.Add(1)
		return next(ctx, cmds)
	}
}

func TestCollabThreadAtomicRedis(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: -1})
	operations := &collabRedisOperations{}
	rdb.AddHook(operations)
	t.Cleanup(func() { _ = rdb.Close() })
	at := time.Now()
	var elapsed time.Duration
	members := func(account int64) []string {
		members, err := rdb.ZRange(context.Background(), fmt.Sprintf("sched:trial:%d:m", account), 0, -1).Result()
		require.NoError(t, err)
		return members
	}
	service.RunCollabAtomicStorage(t, repository.NewGatewayCache(rdb), repository.NewResilienceStore(rdb), repository.NewSchedTrialLeaseCache(rdb), func(d time.Duration) {
		srv.FastForward(d)
		elapsed += d
		srv.SetTime(at.Add(elapsed))
	}, members, operations.count.Load)
	t.Run("selection_interleavings", func(t *testing.T) {
		service.RunCollabInterleavedSelection(t, repository.NewGatewayCache(rdb), repository.NewSchedTrialLeaseCache(rdb), func() {
			require.NoError(t, rdb.FlushDB(context.Background()).Err())
		}, operations.arm, members, operations.count.Load)
	})
}

func TestCollabThreadAliasRedis(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: -1})
	operations := &collabRedisOperations{}
	rdb.AddHook(operations)
	t.Cleanup(func() { _ = rdb.Close() })
	service.RunCollabAliasParity(t, repository.NewGatewayCache(rdb), repository.NewResilienceStore(rdb), repository.NewSchedTrialLeaseCache(rdb), func() {
		require.NoError(t, rdb.FlushDB(context.Background()).Err())
	}, srv.FastForward, func(account int64) []string {
		members, err := rdb.ZRange(context.Background(), fmt.Sprintf("sched:trial:%d:m", account), 0, -1).Result()
		require.NoError(t, err)
		return members
	}, operations.count.Load)
}

func TestCollabThreadScopeRedis(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: -1})
	operations := &collabRedisOperations{}
	rdb.AddHook(operations)
	t.Cleanup(func() { _ = rdb.Close() })
	service.RunCollabScopeRegressions(t, repository.NewGatewayCache(rdb), repository.NewResilienceStore(rdb), repository.NewSchedTrialLeaseCache(rdb), func() {
		require.NoError(t, rdb.FlushDB(context.Background()).Err())
	}, srv.FastForward, operations.count.Load)
}
