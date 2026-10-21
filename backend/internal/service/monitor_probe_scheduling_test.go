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

func (c *probeSlotCache) held(accountID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight[accountID]
}

func probeTestSchedulingConfig(cfg *config.Config) {
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
}

// probeTestAccounts is the minimal reproduction of the monitor plan: account
// 1 (priority 1, concurrency 2, load factor 2) holds two requests, account 2
// (priority 2, concurrency 4, load factor 1) holds one. Both are full by load
// rate; only account 2 has free slots.
func probeTestAccounts(platform string) []Account {
	return []Account{
		{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 1, Concurrency: 2, LoadFactor: intPtr(2), GroupIDs: []int64{probeTestGroupID},
			AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}},
		{ID: 2, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 2, Concurrency: 4, LoadFactor: intPtr(1), GroupIDs: []int64{probeTestGroupID},
			AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}},
	}
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

func TestMonitorProbeOpenAITakesSlotSkippedByLoadRate(t *testing.T) {
	t.Run("regular request keeps the fallback wait", func(t *testing.T) {
		svc, slots, _ := newOpenAIProbeTestService(probeTestAccounts(PlatformOpenAI), map[int64]int{1: 2, 2: 1}, nil)
		selection := selectOpenAIForProbeTest(t, context.Background(), svc, "")
		require.False(t, selection.Acquired)
		require.NotNil(t, selection.WaitPlan)
		require.Equal(t, int64(1), selection.WaitPlan.AccountID)
		require.Equal(t, 30*time.Second, selection.WaitPlan.Timeout)
		require.Zero(t, slots.tried[2], "a request never tries an account that is full by load rate")
		require.Equal(t, 1, slots.held(2))
	})
	t.Run("probe takes the free slot", func(t *testing.T) {
		svc, slots, _ := newOpenAIProbeTestService(probeTestAccounts(PlatformOpenAI), map[int64]int{1: 2, 2: 1}, nil)
		selection := selectOpenAIForProbeTest(t, probeTestContext(), svc, "")
		require.True(t, selection.Acquired)
		require.Nil(t, selection.WaitPlan)
		require.Equal(t, int64(2), selection.Account.ID)
		require.Equal(t, 2, slots.held(2))
		selection.ReleaseFunc()
		require.Equal(t, 1, slots.held(2))
	})
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

func TestMonitorProbeOpenAIWaitsWhenGroupFull(t *testing.T) {
	svc, slots, _ := newOpenAIProbeTestService(probeTestAccounts(PlatformOpenAI), map[int64]int{1: 2, 2: 4}, nil)
	selection := selectOpenAIForProbeTest(t, probeTestContext(), svc, "")
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(1), selection.WaitPlan.AccountID)
	require.Equal(t, 30*time.Second, selection.WaitPlan.Timeout)
	require.Equal(t, 2, slots.held(1))
	require.Equal(t, 4, slots.held(2))
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

func TestMonitorProbeGatewayTakesSlotSkippedByLoadRate(t *testing.T) {
	t.Run("regular request keeps the fallback wait", func(t *testing.T) {
		svc, slots, _ := newGatewayProbeTestService(probeTestAccounts(PlatformAnthropic), map[int64]int{1: 2, 2: 1}, nil, nil)
		result := selectGatewayForProbeTest(t, context.Background(), svc, "", "claude-sonnet-4-5")
		require.False(t, result.Acquired)
		require.NotNil(t, result.WaitPlan)
		require.Equal(t, int64(1), result.WaitPlan.AccountID)
		require.Equal(t, 30*time.Second, result.WaitPlan.Timeout)
		require.Zero(t, slots.tried[2])
	})
	t.Run("probe takes the free slot", func(t *testing.T) {
		svc, slots, _ := newGatewayProbeTestService(probeTestAccounts(PlatformAnthropic), map[int64]int{1: 2, 2: 1}, nil, nil)
		result := selectGatewayForProbeTest(t, probeTestContext(), svc, "", "claude-sonnet-4-5")
		require.True(t, result.Acquired)
		require.Nil(t, result.WaitPlan)
		require.Equal(t, int64(2), result.Account.ID)
		require.Equal(t, 2, slots.held(2))
		result.ReleaseFunc()
	})
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

func TestMonitorProbeGatewayRoutedWaitFallsBackToGroup(t *testing.T) {
	// The routed account 1 is below full load rate (load factor 2) but its
	// only slot is taken; account 2 is not routed and idle.
	accounts := []Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 1, Concurrency: 1, LoadFactor: intPtr(2), AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: 2, Concurrency: 1, AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}},
	}
	routed := func() *Group {
		return &Group{ID: probeTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true,
			ModelRoutingEnabled: true, ModelRouting: map[string][]int64{"claude-sonnet-4-5": {1}}}
	}
	t.Run("regular request waits on the routed account", func(t *testing.T) {
		svc, _, _ := newGatewayProbeTestService(accounts, map[int64]int{1: 1}, nil, routed())
		result := selectGatewayForProbeTest(t, context.Background(), svc, "", "claude-sonnet-4-5")
		require.NotNil(t, result.WaitPlan)
		require.Equal(t, int64(1), result.WaitPlan.AccountID)
		require.Equal(t, 120*time.Second, result.WaitPlan.Timeout)
	})
	t.Run("probe takes a free slot in the group", func(t *testing.T) {
		svc, _, _ := newGatewayProbeTestService(accounts, map[int64]int{1: 1}, nil, routed())
		result := selectGatewayForProbeTest(t, probeTestContext(), svc, "", "claude-sonnet-4-5")
		require.True(t, result.Acquired)
		require.Equal(t, int64(2), result.Account.ID)
		result.ReleaseFunc()
	})
}

func TestMonitorProbeGatewayWaitsWhenGroupFull(t *testing.T) {
	svc, slots, _ := newGatewayProbeTestService(probeTestAccounts(PlatformAnthropic), map[int64]int{1: 2, 2: 4}, nil, nil)
	result := selectGatewayForProbeTest(t, probeTestContext(), svc, "", "claude-sonnet-4-5")
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.Equal(t, int64(1), result.WaitPlan.AccountID)
	require.Equal(t, 30*time.Second, result.WaitPlan.Timeout)
	require.Equal(t, 2, slots.held(1))
	require.Equal(t, 4, slots.held(2))
}

// ---- Share scheduling ----

func TestMonitorProbeShareTakesSlotSkippedByLoadRate(t *testing.T) {
	setup := func() *shareTestEnv {
		env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
		// Account 1 is full by load rate yet has free slots; account 2 is busy.
		env.conc.mockConcurrencyCache.loadMap = map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 3, LoadRate: 100}}
		env.conc.busy[2] = -1
		return env
	}
	t.Run("regular request keeps the fallback wait", func(t *testing.T) {
		env := setup()
		result := env.selectAccount(t, context.Background(), "", nil)
		require.False(t, result.Acquired)
		require.NotNil(t, result.WaitPlan)
		require.Zero(t, env.conc.attempts[1], "a request never tries an account that is full by load rate")
	})
	t.Run("probe takes the free slot", func(t *testing.T) {
		env := setup()
		result := env.selectAccount(t, probeTestContext(), "", nil)
		require.True(t, result.Acquired)
		require.Nil(t, result.WaitPlan)
		require.Equal(t, int64(1), result.Account.ID)
	})
	t.Run("probe waits when every slot is taken", func(t *testing.T) {
		env := setup()
		env.conc.busy[1] = -1
		result := env.selectAccount(t, probeTestContext(), "", nil)
		require.False(t, result.Acquired)
		require.NotNil(t, result.WaitPlan)
		require.Equal(t, 30*time.Second, result.WaitPlan.Timeout)
	})
}

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
