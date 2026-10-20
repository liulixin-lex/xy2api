//go:build unit

package service

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/servertiming"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// meterCall emulates the repository timing hooks: every blocking Redis or
// SQL call is one interval, a pipeline counts once. Both schedulers are then
// measured the same way as in production.
func meterCall(ctx context.Context, metric string) {
	now := time.Now()
	servertiming.Record(ctx, metric, now, now, 1)
}

type meteredCache struct{ *shareTestCache }

func (c meteredCache) GetSessionAccountID(ctx context.Context, groupID int64, key string) (int64, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestCache.GetSessionAccountID(ctx, groupID, key)
}

func (c meteredCache) GetSessionAccountIDWithTTL(ctx context.Context, groupID int64, key string) (int64, time.Duration, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestCache.GetSessionAccountIDWithTTL(ctx, groupID, key)
}

func (c meteredCache) SetSessionAccountID(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) error {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestCache.SetSessionAccountID(ctx, groupID, key, accountID, ttl)
}

func (c meteredCache) SetSessionAccountIDIfAbsent(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) (bool, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestCache.SetSessionAccountIDIfAbsent(ctx, groupID, key, accountID, ttl)
}

func (c meteredCache) RefreshSessionTTL(ctx context.Context, groupID int64, key string, ttl time.Duration) error {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestCache.RefreshSessionTTL(ctx, groupID, key, ttl)
}

func (c meteredCache) DeleteSessionAccountID(ctx context.Context, groupID int64, key string) error {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestCache.DeleteSessionAccountID(ctx, groupID, key)
}

type meteredConcurrency struct{ *shareTestConcurrency }

func (c meteredConcurrency) AcquireAccountSlot(ctx context.Context, accountID int64, limit int, requestID string) (bool, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestConcurrency.AcquireAccountSlot(ctx, accountID, limit, requestID)
}

func (c meteredConcurrency) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestConcurrency.IncrementAccountWaitCount(ctx, accountID, maxWait)
}

func (c meteredConcurrency) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestConcurrency.GetAccountWaitingCount(ctx, accountID)
}

func (c meteredConcurrency) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	meterCall(ctx, servertiming.MetricRedis)
	return c.shareTestConcurrency.GetAccountsLoadBatch(ctx, accounts)
}

type meteredAccountRepo struct{ *mockAccountRepoForPlatform }

func (r meteredAccountRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error) {
	meterCall(ctx, servertiming.MetricDatabase)
	return r.mockAccountRepoForPlatform.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, platforms)
}

func (r meteredAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	meterCall(ctx, servertiming.MetricDatabase)
	return r.mockAccountRepoForPlatform.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func (r meteredAccountRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	meterCall(ctx, servertiming.MetricDatabase)
	return r.mockAccountRepoForPlatform.ListSchedulableByGroupID(ctx, groupID)
}

func (r meteredAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	meterCall(ctx, servertiming.MetricDatabase)
	return r.mockAccountRepoForPlatform.GetByID(ctx, id)
}

type selectionCost struct {
	latency []time.Duration
	redis   int
	db      int64
}

func (c selectionCost) p99() time.Duration {
	sorted := slices.Clone(c.latency)
	slices.Sort(sorted)
	return sorted[len(sorted)*99/100]
}

// newShareCostEnv is a 500-account group in two priorities with load factors
// 100–700, whose Redis and SQL calls are metered; scheduler picks share or
// the original selection.
func newShareCostEnv(t testing.TB, scheduler sharesched.Scheduler) *shareTestEnv {
	t.Helper()
	accounts := make([]Account, 500)
	for i := range accounts {
		load := 100 * (1 + i%7)
		accounts[i] = Account{ID: int64(i + 1), Priority: 1 + i%2, Concurrency: 1_000, LoadFactor: &load}
	}
	env := newShareTestEnv(t, shareTestPolicy(scheduler), accounts...)
	env.gateway.cache = meteredCache{env.cache}
	env.gateway.concurrencyService = NewConcurrencyService(meteredConcurrency{env.conc})
	env.gateway.accountRepo = meteredAccountRepo{env.repo}
	return env
}

// runCost replays the same workload: a returning session four times in five,
// otherwise a new one, each with a long cacheable prompt.
func runCost(t *testing.T, env *shareTestEnv, requests int) selectionCost {
	t.Helper()
	rng := rand.New(rand.NewPCG(1, 2))
	var out selectionCost
	sessions := 0
	gid := shareTestGroupID
	for i := range requests {
		key := fmt.Sprintf("cost-%d", sessions)
		if sessions == 0 || rng.IntN(5) == 0 {
			sessions++
			key = fmt.Sprintf("cost-%d", sessions)
		} else {
			key = fmt.Sprintf("cost-%d", rng.IntN(sessions)+1)
		}
		collector := servertiming.New(time.Now())
		ctx := servertiming.WithCollector(cacheableContext(), collector)
		start := time.Now()
		result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, key, "claude-sonnet-4-5", nil, "", 0)
		elapsed := time.Since(start)
		require.NoError(t, err, "request %d", i)
		require.True(t, result.Acquired)
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
		out.latency = append(out.latency, elapsed)
		out.redis += collector.Summary(servertiming.MetricRedis).RoundTrips
		out.db += collector.Summary(servertiming.MetricDatabase).Count
	}
	return out
}

// TestShareSchedulingSelectionCostAt500Accounts checks the selection budget
// on a 500-account group: no database query over the original scheduler,
// and Redis round trips within its load-cache noise. Latency is left to the
// benchmark below; CI runs this test under the race detector.
func TestShareSchedulingSelectionCostAt500Accounts(t *testing.T) {
	const requests = 3000
	original := runCost(t, newShareCostEnv(t, sharesched.SchedulerSub2API), requests)
	share := runCost(t, newShareCostEnv(t, sharesched.SchedulerShare), requests)

	t.Logf("p99 original=%s share=%s; redis round trips original=%d share=%d; db queries original=%d share=%d",
		original.p99(), share.p99(), original.redis, share.redis, original.db, share.db)
	require.Positive(t, original.db)
	require.LessOrEqual(t, share.db, original.db)
	// Both read account loads for new placements through the same short
	// cache (design §5), so their counts differ only by how often the cache
	// expires during the run.
	require.LessOrEqual(t, share.redis, original.redis+original.redis/100)
}

// BenchmarkShareSchedulingSelection500 compares one selection on a
// 500-account group; the share overhead budget is 1ms at P99.
//
//	go test -tags=unit ./internal/service -run '^$' -bench ShareSchedulingSelection500
func BenchmarkShareSchedulingSelection500(b *testing.B) {
	for _, scheduler := range []sharesched.Scheduler{sharesched.SchedulerSub2API, sharesched.SchedulerShare} {
		b.Run(string(scheduler), func(b *testing.B) {
			env := newShareCostEnv(b, scheduler)
			gid := shareTestGroupID
			ctx := cacheableContext()
			b.ResetTimer()
			for i := range b.N {
				result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, fmt.Sprintf("bench-%d", i%2000), "claude-sonnet-4-5", nil, "", 0)
				if err != nil {
					b.Fatal(err)
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			}
		})
	}
}

func (e *shareTestEnv) kindCounters(kind string) map[string]int64 {
	e.share.stats.mu.Lock()
	defer e.share.stats.mu.Unlock()
	out := map[string]int64{}
	for key, v := range e.share.stats.counters {
		if key.Kind == kind {
			out[key.Field] += v
		}
	}
	return out
}
