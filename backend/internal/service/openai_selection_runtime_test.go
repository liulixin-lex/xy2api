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

func (r *selectionRuntimeRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]Account, error) {
	r.lists++
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group, platform)
}
func (r *selectionRuntimeRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

type selectionRuntimeCache struct {
	schedulerTestGatewayCache
	remaining        time.Duration
	ttlKeys, casKeys []string
}

func (c *selectionRuntimeCache) GetSessionAccountIDWithTTL(ctx context.Context, group int64, key string) (int64, time.Duration, error) {
	c.ttlKeys = append(c.ttlKeys, key)
	id, err := c.GetSessionAccountID(ctx, group, key)
	return id, c.remaining, err
}
func (c *selectionRuntimeCache) CompareAndSwapSessionAccountID(_ context.Context, _ int64, key string, old, next int64, _ time.Duration) (bool, error) {
	c.casKeys = append(c.casKeys, key)
	if c.sessionBindings[key] != old {
		return false, nil
	}
	c.sessionBindings[key] = next
	return true, nil
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
	f.svc = &OpenAIGatewayService{cfg: cfg, cache: f.cache, accountRepo: f.repo, concurrencyService: NewConcurrencyService(f.loads)}
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
			a, b, c, d := gateV3Account(1, 1), gateV3Account(2, 2), gateV3Account(3, 2), gateV3Account(4, 3)
			a.RateMultiplier, b.RateMultiplier, c.RateMultiplier, d.RateMultiplier = floatPtr(0), floatPtr(.8), floatPtr(.2), floatPtr(0)
			f := newSelectionRuntimeFixture(t, a, b, c, d)
			f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{State: schedquality.StateCooling}
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
		f := newSelectionRuntimeFixture(t, gateV3Account(1, 1), gateV3Account(2, 2))
		for _, id := range []int64{1, 2} {
			f.view.States[fakeSchedViewKey{7, id, "m"}] = schedquality.StateView{State: schedquality.StateCooling}
		}
		require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", nil).Account.ID)
	})
}

func TestOpenAISelectionRuntimeSessionCacheAndOwnerSlotOrder(t *testing.T) {
	for _, mode := range []string{"immediate", "idle", "migration"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", mode, legacy), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, gateV3Account(10, 5), gateV3Account(1, 1))
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
					f.view.States[fakeSchedViewKey{7, 10, "m"}] = schedquality.StateView{State: schedquality.StateCooling}
				}
				result := f.selectNow(t, ctx, "session", nil)
				if legacy {
					require.Equal(t, int64(10), result.Account.ID)
					require.Equal(t, int64(10), f.cache.sessionBindings[key])
					require.Zero(t, f.cache.sessionBindings["openai:session"])
					if mode != "idle" {
						require.Equal(t, []int64{1, 10}, f.attempts)
						require.Equal(t, []int64{1}, f.releases)
					}
				} else {
					require.Equal(t, int64(1), result.Account.ID)
					require.Equal(t, int64(1), f.cache.sessionBindings[key])
					require.Equal(t, []int64{1}, f.attempts, "migration runs before taking the owner slot")
				}
				for _, key := range append(f.cache.ttlKeys, f.cache.casKeys...) {
					require.Equal(t, "openai:session", key)
				}
			})
		}
	}
}

func TestOpenAISelectionRuntimeCandidatePoolAndLatestView(t *testing.T) {
	t.Run("no better member avoids admission pool and loads", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, gateV3Account(10, 1), gateV3Account(1, 2))
		f.cache.sessionBindings["openai:session"] = 10
		require.Equal(t, int64(10), f.selectNow(t, context.Background(), "session", nil).Account.ID)
		require.Zero(t, f.loads.batches)
		require.Equal(t, 1, f.repo.lists, "membership is read without building a load pool")
	})
	t.Run("exclude and hard admission and full observation", func(t *testing.T) {
		bad := gateV3Account(2, 1)
		bad.Schedulable = false
		f := newSelectionRuntimeFixture(t, gateV3Account(10, 5), gateV3Account(1, 0), bad, gateV3Account(3, 2), gateV3Account(4, 3))
		f.cache.sessionBindings["openai:session"] = 10
		f.loads.loadMap = map[int64]*AccountLoadInfo{3: {LoadRate: 100, CurrentConcurrency: 10}}
		require.Equal(t, int64(4), f.selectNow(t, context.Background(), "session", map[int64]struct{}{1: {}}).Account.ID)
		require.Equal(t, []int64{4}, f.attempts)
		require.True(t, f.svc.selectionFullMarks.Recently(3, f.svc.selectionNow()))
	})
	t.Run("latest view rejects after slot acquisition", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, gateV3Account(10, 5), gateV3Account(1, 1))
		f.cache.sessionBindings["openai:session"] = 10
		f.loads.after = func(id int64) {
			if id == 1 {
				f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{State: schedquality.StateCooling}
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
				f := newSelectionRuntimeFixture(t, gateV3Account(10, 1), gateV3Account(1, 2))
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
