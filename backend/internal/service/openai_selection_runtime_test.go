//go:build unit

package service

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type selectionRuntimeRepo struct {
	schedulerTestOpenAIAccountRepo
	lists int
}

func (r *selectionRuntimeRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *selectionRuntimeRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]Account, error) {
	r.lists++
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group, platform)
}
func (r *selectionRuntimeRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

type selectionRuntimeCache struct {
	store ResilienceStore
	schedulerTestGatewayCache
	remaining        time.Duration
	ttlKeys, casKeys []string
}

func (c *selectionRuntimeCache) GetSessionAccountIDWithTTL(ctx context.Context, group int64, key string) (int64, time.Duration, error) {
	c.ttlKeys = append(c.ttlKeys, key)
	id, err := c.GetSessionAccountID(ctx, group, key)
	return id, c.remaining, err
}
func (c *selectionRuntimeCache) CommitSessionBinding(_ context.Context, _ int64, key string, expected StickySessionBinding, next int64, _ time.Duration) (bool, error) {
	c.casKeys = append(c.casKeys, key)
	if c.sessionBindings[key] != expected.AccountID {
		return false, nil
	}
	c.sessionBindings[key] = next
	if expected.LegacyKey != "" && c.sessionBindings[expected.LegacyKey] != 0 {
		c.sessionBindings[expected.LegacyKey] = next
	}
	return true, nil
}

func (c *selectionRuntimeCache) ResolveSessionBinding(ctx context.Context, group int64, primary, legacy string, fallback bool) (StickySessionBinding, error) {
	if c.sessionBindings[primary] == 0 && fallback {
		c.sessionBindings[primary] = c.sessionBindings[legacy]
	}
	binding, err := c.GetSessionBinding(ctx, group, primary)
	binding.LegacyKey = legacy
	if c.sessionBindings[legacy] != 0 && binding.AccountID != 0 {
		c.sessionBindings[legacy] = binding.AccountID
	}
	return binding, err
}

func (c *selectionRuntimeCache) SyncSessionBindingAlias(_ context.Context, _ int64, primary, legacy string, owner int64, _ time.Duration, create bool) error {
	current := c.sessionBindings[primary]
	if current > 0 && (owner == 0 || current == owner) && (create || c.sessionBindings[legacy] != 0) {
		c.sessionBindings[legacy] = current
	}
	return nil
}

func (c *selectionRuntimeCache) DeleteSessionBindingAliases(_ context.Context, _ int64, primary, legacy string, owner int64) (bool, error) {
	current := c.sessionBindings[primary]
	if current == 0 {
		current = c.sessionBindings[legacy]
	}
	if owner != 0 && current != owner {
		return false, nil
	}
	delete(c.sessionBindings, primary)
	delete(c.sessionBindings, legacy)
	return current != 0, nil
}

func (c *selectionRuntimeCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	id, err := c.GetSessionAccountID(ctx, group, key)
	return StickySessionBinding{AccountID: id, Generation: "g1"}, err
}

type selectionRuntimeLoads struct {
	schedulerTestConcurrencyCache
	batches int
	after   func(int64)
}

func (c *selectionRuntimeLoads) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.batches++
	return c.schedulerTestConcurrencyCache.GetAccountsLoadBatch(ctx, accounts)
}
func (c *selectionRuntimeLoads) AcquireAccountSlot(ctx context.Context, id int64, limit int, key string) (bool, error) {
	acquired, err := c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, id, limit, key)
	if c.after != nil {
		c.after(id)
	}
	return acquired, err
}

type selectionRuntimeFixture struct {
	svc                *OpenAIGatewayService
	repo               *selectionRuntimeRepo
	cache              *selectionRuntimeCache
	loads              *selectionRuntimeLoads
	view               *fakeSchedView
	attempts, releases []int64
}

func newSelectionRuntimeFixture(t *testing.T, accounts ...Account) *selectionRuntimeFixture {
	t.Helper()
	f := &selectionRuntimeFixture{repo: &selectionRuntimeRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}, cache: &selectionRuntimeCache{schedulerTestGatewayCache: schedulerTestGatewayCache{sessionBindings: map[string]int64{}}, remaining: 29 * time.Minute}}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
	f.loads = &selectionRuntimeLoads{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{acquiredIDs: &f.attempts, releasedIDs: &f.releases}}
	f.svc = &OpenAIGatewayService{cfg: cfg, cache: f.cache, accountRepo: f.repo, resilience: NewResilienceService(nil, newMemResilienceStore()), concurrencyService: NewConcurrencyService(f.loads)}
	f.cache.store = f.svc.resilience.store
	now := time.Now()
	f.view = selectionTestView(now)
	board := NewOpenAISelectionBoard(f.svc, func() schedquality.View { return f.view })
	board.now = func() time.Time { return now }
	return f
}
func (f *selectionRuntimeFixture) selectNow(t *testing.T, ctx context.Context, session string, excluded map[int64]struct{}) *AccountSelectionResult {
	t.Helper()
	ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
	result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, result)
	CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
	t.Cleanup(func() {
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	})
	return result
}

func TestOpenAISelectionRuntimeStandingPriorityRateAndBoard(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint("load_unavailable=", unavailable), func(t *testing.T) {
			a, b, c, d := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 2), selectionFixtureAccount(4, 3)
			a.RateMultiplier, b.RateMultiplier, c.RateMultiplier, d.RateMultiplier = floatPtr(0), floatPtr(.8), floatPtr(.2), floatPtr(0)
			f := newSelectionRuntimeFixture(t, a, b, c, d)
			f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Failing: true}
			f.loads.acquireResults = map[int64]bool{1: true, 2: false, 3: false, 4: false}
			f.loads.loadMap = map[int64]*AccountLoadInfo{3: {LoadRate: 90}, 2: {LoadRate: 0}, 4: {LoadRate: 0}}
			if unavailable {
				f.loads.loadBatchErr = fmt.Errorf("load unavailable")
			}
			bindings := maps.Clone(f.cache.sessionBindings)
			rows, err := f.svc.selectionBoard.Board(context.Background(), 7, "m")
			require.NoError(t, err)
			require.Empty(t, f.attempts)
			require.Empty(t, f.svc.selectionFullMarks.at)
			require.Equal(t, bindings, f.cache.sessionBindings)
			var ids []int64
			for _, row := range rows {
				ids = append(ids, row.AccountID)
			}
			require.Equal(t, []int64{3, 2, 4, 1}, ids)
			result := f.selectNow(t, context.Background(), "", nil)
			require.Equal(t, int64(1), result.Account.ID)
			require.Equal(t, ids, f.attempts, "D11 board and actual slot attempt order")
		})
	}
	t.Run("all back still selects", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
		for _, id := range []int64{1, 2} {
			f.view.States[fakeSchedViewKey{7, id, "m"}] = schedquality.StateView{Failing: true}
		}
		require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", nil).Account.ID)
	})
}

func TestOpenAISelectionRuntimeSessionCacheAndOwnerSlotOrder(t *testing.T) {
	for _, mode := range []string{"immediate", "idle", "migration"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", mode, legacy), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
				key := "openai:session"
				ctx := context.Background()
				if legacy {
					key = "openai:old"
					ctx = withOpenAILegacySessionHash(ctx, "old")
				}
				f.cache.sessionBindings[key] = 10
				if mode == "idle" {
					f.view.Takeovers[7] = fakeSchedTakeover{Enabled: true, Idle: time.Minute}
					f.cache.remaining = f.svc.openAIStickyBindingTTL(ptrInt64Selection(7)) - 2*time.Minute
				}
				if mode == "migration" {
					f.view.States[fakeSchedViewKey{7, 10, "m"}] = schedquality.StateView{Failing: true}
				}
				result := f.selectNow(t, ctx, "session", nil)
				require.Equal(t, int64(1), result.Account.ID)
				require.Equal(t, int64(1), f.cache.sessionBindings[key])
				require.Equal(t, []int64{1}, f.attempts, "migration runs before taking the owner slot")
				if legacy {
					require.EqualValues(t, 1, f.cache.sessionBindings["openai:session"])
				}
				for _, actualKey := range append(f.cache.ttlKeys, f.cache.casKeys...) {
					require.Equal(t, "openai:session", actualKey, "all requests use the canonical primary binding")
				}
			})
		}
	}
}

func TestOpenAISelectionRuntimeCandidatePoolAndLatestView(t *testing.T) {
	t.Run("no better member avoids admission pool and loads", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(1, 2))
		f.cache.sessionBindings["openai:session"] = 10
		require.Equal(t, int64(10), f.selectNow(t, context.Background(), "session", nil).Account.ID)
		require.Zero(t, f.loads.batches)
		require.Equal(t, 1, f.repo.lists, "membership is read without building a load pool")
	})
	t.Run("exclude and hard admission and full observation", func(t *testing.T) {
		bad := selectionFixtureAccount(2, 1)
		bad.Schedulable = false
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 0), bad, selectionFixtureAccount(3, 2), selectionFixtureAccount(4, 3))
		f.cache.sessionBindings["openai:session"] = 10
		f.loads.loadMap = map[int64]*AccountLoadInfo{3: {LoadRate: 100, CurrentConcurrency: 10}}
		require.Equal(t, int64(4), f.selectNow(t, context.Background(), "session", map[int64]struct{}{1: {}}).Account.ID)
		require.Equal(t, []int64{4}, f.attempts)
		require.True(t, f.svc.selectionFullMarks.Recently(3, f.svc.selectionNow()))
	})
	t.Run("latest view rejects after slot acquisition", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
		f.cache.sessionBindings["openai:session"] = 10
		f.loads.after = func(id int64) {
			if id == 1 {
				f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Failing: true}
			}
		}
		require.Equal(t, int64(10), f.selectNow(t, context.Background(), "session", nil).Account.ID)
		require.Equal(t, []int64{1}, f.releases)
		require.Empty(t, f.cache.casKeys)
	})
}

func TestOpenAISelectionRuntimeFullOwnerRetryBorrowQueue(t *testing.T) {
	for _, mode := range []string{"short retry", "borrow", "queue"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(1, 2))
				f.cache.sessionBindings["openai:session"] = 10
				f.loads.acquireResults = map[int64]bool{10: false, 1: mode != "queue"}
				f.loads.after = func(id int64) {
					if mode == "short retry" && id == 10 {
						f.loads.acquireResults[10] = true
					}
				}
				started := time.Now()
				result := f.selectNow(t, context.Background(), "session", nil)
				require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
				require.Empty(t, f.cache.casKeys)
				switch mode {
				case "short retry":
					require.True(t, result.Acquired)
					require.Equal(t, int64(10), result.Account.ID)
					require.Equal(t, []int64{10, 10}, f.attempts)
					require.Less(t, time.Since(started), openAIStickyCapacityRetryDuration)
				case "borrow":
					require.True(t, result.Acquired)
					require.True(t, result.StickyCapacitySpillover)
					require.Equal(t, int64(1), result.Account.ID)
					require.GreaterOrEqual(t, time.Since(started), openAIStickyCapacityRetryDuration)
				case "queue":
					require.False(t, result.Acquired)
					require.NotNil(t, result.WaitPlan)
					require.Equal(t, int64(10), result.WaitPlan.AccountID)
					require.GreaterOrEqual(t, time.Since(started), openAIStickyCapacityRetryDuration)
				}
			})
		})
	}
}

func (c *selectionRuntimeCache) RecordSessionBindingFailure(ctx context.Context, group int64, key, session string, expected StickySessionBinding, target int64, ttl time.Duration, resetAt ...time.Time) (bool, error) {
	if c.sessionBindings[key] != expected.AccountID {
		return false, nil
	}
	return true, c.store.AddSessionAvoid(ctx, group, session, target, ttl, resetAt...)
}
