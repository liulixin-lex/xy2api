package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type runtimeCacheTestStub struct {
	service.ConcurrencyCache
	peek   func(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error)
	groups func(context.Context, []int64) (map[int64]map[int64]int, error)
}

func (s runtimeCacheTestStub) PeekAccountsLoadBatch(ctx context.Context, a []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	return s.peek(ctx, a)
}
func (s runtimeCacheTestStub) GetAccountGroupConcurrencyBatch(ctx context.Context, a []int64) (map[int64]map[int64]int, error) {
	return s.groups(ctx, a)
}
func TestRuntimeReadFailureNeverBecomesZero(t *testing.T) {
	q, e := newReaderQuery(QueryRequest{Tool: "xy2api.ops.get_concurrency_snapshot", Actor: testReaderActor()}, time.Now())
	require.NoError(t, e)
	accounts := []map[string]any{{"account_id": int64(1), "capacity": int64(10)}}
	cacheError := errors.New("redis failed")
	r := &runtimeReader{cache: runtimeCacheTestStub{peek: func(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
		return nil, cacheError
	}}}
	require.ErrorIs(t, r.concurrency(context.Background(), q, accounts), cacheError)
	require.Empty(t, q.result.Items)
	r.cache = runtimeCacheTestStub{peek: func(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
		return map[int64]*service.AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 2}}, nil
	}, groups: func(context.Context, []int64) (map[int64]map[int64]int, error) { return nil, cacheError }}
	require.ErrorIs(t, r.concurrency(context.Background(), q, accounts), cacheError)
	require.Empty(t, q.result.Items)
	r.cache = runtimeCacheTestStub{peek: func(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
		return map[int64]*service.AccountLoadInfo{}, nil
	}, groups: func(context.Context, []int64) (map[int64]map[int64]int, error) { return nil, nil }}
	require.ErrorIs(t, r.concurrency(context.Background(), q, accounts), ErrUnavailable)
}
func TestRuntimeReadPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q, e := newReaderQuery(QueryRequest{Tool: "xy2api.ops.get_concurrency_snapshot", Actor: testReaderActor()}, time.Now())
	require.NoError(t, e)
	r := &runtimeReader{cache: runtimeCacheTestStub{peek: func(c context.Context, _ []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
		require.ErrorIs(t, c.Err(), context.Canceled)
		return nil, c.Err()
	}}}
	require.ErrorIs(t, r.concurrency(ctx, q, []map[string]any{{"account_id": int64(1), "capacity": int64(10)}}), context.Canceled)
}
