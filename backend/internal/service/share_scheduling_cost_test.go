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

// newShareCostEnv is a 500-account group whose Redis and SQL calls are
// metered; scheduler picks share or the original selection.
func newShareCostEnv(t testing.TB, scheduler sharesched.Scheduler) *shareTestEnv {
	t.Helper()
	accounts := make([]Account, 500)
	rules := make([]sharesched.AccountRule, 500)
	for i := range accounts {
		id := int64(i + 1)
		accounts[i] = Account{ID: id, Priority: 1, Concurrency: 1_000}
		rules[i] = sharesched.AccountRule{AccountID: id, Tier: 1 + i%2, Weight: 1 + i%7}
	}
	env := newShareTestEnv(t, shareTestPolicy(scheduler, rules...), accounts...)
	env.gateway.cache = meteredCache{env.cache}
	env.gateway.concurrencyService = NewConcurrencyService(meteredConcurrency{env.conc})
	env.gateway.accountRepo = meteredAccountRepo{env.repo}
	// As with server.enable_server_timing, which installs the timing hooks.
	env.share.meterIO = true
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

// TestShareSchedulingSelectionCostAt500Accounts checks the design §12
// budget on a 500-account group: no database query and no Redis round trip
// over the original scheduler. Latency is left to the benchmark below; CI
// runs this test under the race detector.
func TestShareSchedulingSelectionCostAt500Accounts(t *testing.T) {
	const requests = 3000
	original := runCost(t, newShareCostEnv(t, sharesched.SchedulerSub2API), requests)
	share := runCost(t, newShareCostEnv(t, sharesched.SchedulerShare), requests)

	t.Logf("p99 original=%s share=%s; redis round trips original=%d share=%d; db queries original=%d share=%d",
		original.p99(), share.p99(), original.redis, share.redis, original.db, share.db)
	require.Positive(t, original.db)
	require.LessOrEqual(t, share.db, original.db)
	require.LessOrEqual(t, share.redis, original.redis)
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

func TestShareSchedulingRecordsSampledSelectionCost(t *testing.T) {
	env := newShareCostEnv(t, sharesched.SchedulerShare)
	for i := range 20 {
		env.selectAccount(t, cacheableContext(), fmt.Sprintf("sel-%d", i), nil)
	}
	original := newShareCostEnv(t, sharesched.SchedulerSub2API)
	for i := range 10 {
		original.selectAccount(t, cacheableContext(), fmt.Sprintf("orig-%d", i), nil)
	}
	share := shareSelectionCosts(env.kindCounters(shareStatsKindCache))["share"]
	require.Equal(t, int64(20), share.Samples)
	require.Equal(t, int64(20), share.IOSamples)
	require.NotNil(t, share.AvgRedisTrips)
	require.Equal(t, 3.0, *share.AvgRedisTrips, "binding read, slot and binding write")
	require.Positive(t, share.AvgMs)
	require.NotNil(t, share.AvgDBQueries)
	require.Positive(t, *share.AvgDBQueries, "the candidate listing")
	base := shareSelectionCosts(original.kindCounters(shareStatsKindCache))
	require.Equal(t, int64(10), base["original"].Samples)
	require.NotContains(t, base, "share")
}

func TestShareSchedulingSelectionCostWithoutTimingHooks(t *testing.T) {
	// Without server timing the Redis and database calls are invisible:
	// the cost keeps its duration but must not report zero round trips.
	env := newShareCostEnv(t, sharesched.SchedulerShare)
	env.share.meterIO = false
	for i := range 10 {
		env.selectAccount(t, cacheableContext(), fmt.Sprintf("plain-%d", i), nil)
	}
	share := shareSelectionCosts(env.kindCounters(shareStatsKindCache))["share"]
	require.Equal(t, int64(10), share.Samples)
	require.Positive(t, share.AvgMs)
	require.Zero(t, share.IOSamples)
	require.Nil(t, share.AvgRedisTrips)
	require.Nil(t, share.AvgRedisMs)
	require.Nil(t, share.AvgDBQueries)

	// A node with the hooks adds figures averaged over its samples only.
	env.share.meterIO = true
	for i := range 5 {
		env.selectAccount(t, cacheableContext(), fmt.Sprintf("metered-%d", i), nil)
	}
	share = shareSelectionCosts(env.kindCounters(shareStatsKindCache))["share"]
	require.Equal(t, int64(15), share.Samples)
	require.Equal(t, int64(5), share.IOSamples)
	require.NotNil(t, share.AvgRedisTrips)
	require.Equal(t, 3.0, *share.AvgRedisTrips)
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
