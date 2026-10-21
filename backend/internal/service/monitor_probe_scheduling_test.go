//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/stretchr/testify/require"
)

const probeTestGroupID = int64(4207)

func probeTestContext() context.Context {
	return monitorprobe.WithProbe(context.Background(), 11)
}

// probeSlotCache models account slots as the Redis cache does: a slot is free
// while fewer than maxConcurrency requests hold one, and the load rate counts
// holders and waiters against the load factor.
type probeSlotCache struct {
	*mockConcurrencyCache
	mu       sync.Mutex
	inFlight map[int64]int
	waiting  map[int64]int
	tried    map[int64]int
}

func newProbeSlotCache(inFlight map[int64]int) *probeSlotCache {
	return &probeSlotCache{mockConcurrencyCache: &mockConcurrencyCache{}, inFlight: inFlight,
		waiting: map[int64]int{}, tried: map[int64]int{}}
}

func (c *probeSlotCache) AcquireAccountSlot(_ context.Context, accountID int64, maxConcurrency int, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tried[accountID]++
	if c.inFlight[accountID] >= maxConcurrency {
		return false, nil
	}
	c.inFlight[accountID]++
	return true, nil
}

func (c *probeSlotCache) ReleaseAccountSlot(_ context.Context, accountID int64, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight[accountID]--
	return nil
}

func (c *probeSlotCache) GetAccountWaitingCount(_ context.Context, accountID int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiting[accountID], nil
}

func (c *probeSlotCache) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, acc := range accounts {
		current, waiting := c.inFlight[acc.ID], c.waiting[acc.ID]
		rate := 0
		if acc.MaxConcurrency > 0 {
			rate = (current + waiting) * 100 / acc.MaxConcurrency
		}
		out[acc.ID] = &AccountLoadInfo{AccountID: acc.ID, CurrentConcurrency: current, WaitingCount: waiting, LoadRate: rate}
	}
	return out, nil
}

func probeTestSchedulingConfig(cfg *config.Config) {
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
}

// ---- OpenAI default scheduler ----

func newOpenAIProbeTestService(accounts []Account, inFlight map[int64]int, bindings map[string]int64) (*OpenAIGatewayService, *probeSlotCache, *schedulerTestGatewayCache) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	cfg := &config.Config{}
	probeTestSchedulingConfig(cfg)
	slots := newProbeSlotCache(inFlight)
	cache := &schedulerTestGatewayCache{sessionBindings: bindings}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(slots),
	}
	return svc, slots, cache
}

func selectOpenAIForProbeTest(t *testing.T, ctx context.Context, svc *OpenAIGatewayService, sessionHash string) *AccountSelectionResult {
	t.Helper()
	gid := probeTestGroupID
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &gid, "", sessionHash, "gpt-5.4", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	return selection
}

func TestMonitorProbeOpenAIIgnoresStickyBinding(t *testing.T) {
	// Account 1 is bound to the session and full; account 2 is idle.
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 1, Concurrency: 1, GroupIDs: []int64{probeTestGroupID}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 2, Concurrency: 1, GroupIDs: []int64{probeTestGroupID}},
	}
	t.Run("regular request waits for its bound account", func(t *testing.T) {
		svc, _, cache := newOpenAIProbeTestService(accounts, map[int64]int{1: 1}, map[string]int64{"openai:sess": 1})
		selection := selectOpenAIForProbeTest(t, context.Background(), svc, "sess")
		require.NotNil(t, selection.WaitPlan)
		require.Equal(t, int64(1), selection.WaitPlan.AccountID)
		require.Equal(t, 120*time.Second, selection.WaitPlan.Timeout)
		require.Equal(t, map[string]int64{"openai:sess": 1}, cache.sessionBindings)
	})
	t.Run("probe takes the idle account and binds nothing", func(t *testing.T) {
		svc, _, cache := newOpenAIProbeTestService(accounts, map[int64]int{1: 1}, map[string]int64{"openai:sess": 1})
		ctx := probeTestContext()
		selection := selectOpenAIForProbeTest(t, ctx, svc, "sess")
		require.True(t, selection.Acquired)
		require.Equal(t, int64(2), selection.Account.ID)
		gid := probeTestGroupID
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "sess", 2))
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "fresh", 2))
		require.Equal(t, map[string]int64{"openai:sess": 1}, cache.sessionBindings)
		selection.ReleaseFunc()
	})
}

func TestMonitorProbeOpenAISessionHashIsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"fixed","input":"hi"}`)
	hash := func(ctx context.Context) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
		c.Request.Header.Set("session_id", "fixed-session")
		svc := &OpenAIGatewayService{}
		return svc.GenerateSessionHash(c, body) + "|" + svc.GenerateSessionHashWithFallback(c, body, "ws-seed")
	}
	require.NotEqual(t, "|", hash(context.Background()), "a regular request keeps its session")
	require.Equal(t, "|", hash(probeTestContext()), "a probe has no session")
}

// ---- Anthropic gateway scheduler ----

func newGatewayProbeTestService(accounts []Account, inFlight map[int64]int, bindings map[string]int64, group *Group) (*GatewayService, *probeSlotCache, *mockGatewayCacheForPlatform) {
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	if group == nil {
		group = &Group{ID: probeTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	}
	cfg := testConfig()
	probeTestSchedulingConfig(cfg)
	slots := newProbeSlotCache(inFlight)
	if bindings == nil {
		bindings = map[string]int64{}
	}
	cache := &mockGatewayCacheForPlatform{sessionBindings: bindings}
	svc := &GatewayService{
		accountRepo:        repo,
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{probeTestGroupID: group}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(slots),
	}
	return svc, slots, cache
}

func selectGatewayForProbeTest(t *testing.T, ctx context.Context, svc *GatewayService, sessionHash, model string) *AccountSelectionResult {
	t.Helper()
	gid := probeTestGroupID
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &gid, sessionHash, model, nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	return result
}

func TestMonitorProbeGatewayIgnoresStickyBinding(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 1, Concurrency: 1, AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 2, Concurrency: 1, AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}},
	}
	gid := probeTestGroupID
	t.Run("regular request waits for its bound account", func(t *testing.T) {
		svc, _, cache := newGatewayProbeTestService(accounts, map[int64]int{1: 1}, map[string]int64{"sess": 1}, nil)
		bound, err := svc.GetCachedSessionAccountID(context.Background(), &gid, "sess")
		require.NoError(t, err)
		require.Equal(t, int64(1), bound)
		result := selectGatewayForProbeTest(t, context.Background(), svc, "sess", "claude-sonnet-4-5")
		require.NotNil(t, result.WaitPlan)
		require.Equal(t, int64(1), result.WaitPlan.AccountID)
		require.Equal(t, 120*time.Second, result.WaitPlan.Timeout)
		require.Equal(t, map[string]int64{"sess": 1}, cache.sessionBindings)
	})
	t.Run("probe takes the idle account and binds nothing", func(t *testing.T) {
		svc, _, cache := newGatewayProbeTestService(accounts, map[int64]int{1: 1}, map[string]int64{"sess": 1}, nil)
		ctx := probeTestContext()
		bound, err := svc.GetCachedSessionAccountID(ctx, &gid, "sess")
		require.NoError(t, err)
		require.Zero(t, bound, "a probe reads no binding")
		result := selectGatewayForProbeTest(t, ctx, svc, "sess", "claude-sonnet-4-5")
		require.True(t, result.Acquired)
		require.Equal(t, int64(2), result.Account.ID)
		require.NoError(t, svc.BindStickySession(ctx, &gid, "sess", 2))
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "fresh", 2))
		require.Equal(t, map[string]int64{"sess": 1}, cache.sessionBindings)
		result.ReleaseFunc()
	})
}

// ---- Share scheduling ----

func TestMonitorProbeShareSkipsAffinityWait(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "wait")
	env.cache.bind("wait", target, time.Hour-30*time.Second)
	env.conc.busy[target] = 2

	start := time.Now()
	result := env.selectAccount(t, monitorprobe.WithProbe(cacheableContext(), 11), "wait", nil)
	require.True(t, result.Acquired)
	require.Equal(t, other, result.Account.ID, "a probe does not wait for a bound account")
	require.Less(t, time.Since(start), 200*time.Millisecond)
	require.Zero(t, env.conc.maxSeen[target], "a probe never joins the account's queue")
	require.Equal(t, target, env.cache.sessionBindings["wait"], "the binding stays")
	require.Empty(t, env.cache.created)
	require.Empty(t, env.cache.replaced)
	require.Empty(t, env.cache.refreshed)
}
