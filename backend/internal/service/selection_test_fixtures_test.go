//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

const selectionTestGroupID = int64(7)

// selectionTestCache adds the TTL read and SET NX of the Redis gateway cache and
// records every binding write.
type selectionTestCache struct {
	*mockGatewayCacheForPlatform
	mu        sync.Mutex
	remaining map[string]time.Duration
	ttls      map[string]time.Duration
	refreshed []string
	replaced  []string
	created   []string
}

func newSelectionTestCache() *selectionTestCache {
	return &selectionTestCache{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		remaining: map[string]time.Duration{}, ttls: map[string]time.Duration{}}
}

func (c *selectionTestCache) bind(key string, accountID int64, remaining time.Duration) {
	c.sessionBindings[key] = accountID
	c.remaining[key] = remaining
}

func (c *selectionTestCache) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.sessionBindings[key]; ok {
		return id, nil
	}
	return 0, ErrStickySessionNotFound
}

func (c *selectionTestCache) GetSessionAccountIDWithTTL(_ context.Context, _ int64, key string) (int64, time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.sessionBindings[key]
	if !ok {
		return 0, 0, ErrStickySessionNotFound
	}
	return id, c.remaining[key], nil
}

func (c *selectionTestCache) SetSessionAccountID(_ context.Context, _ int64, key string, accountID int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionBindings[key], c.ttls[key] = accountID, ttl
	c.replaced = append(c.replaced, key)
	return nil
}

func (c *selectionTestCache) SetSessionAccountIDIfAbsent(_ context.Context, _ int64, key string, accountID int64, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sessionBindings[key]; ok {
		return false, nil
	}
	c.sessionBindings[key], c.ttls[key] = accountID, ttl
	c.created = append(c.created, key)
	return true, nil
}

func (c *selectionTestCache) RefreshSessionTTL(_ context.Context, _ int64, key string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttls[key] = ttl
	c.refreshed = append(c.refreshed, key)
	return nil
}

// selectionTestConcurrency rejects the first busy[id] slot requests of an
// account and tracks the wait queue.
type selectionTestConcurrency struct {
	*mockConcurrencyCache
	mu       sync.Mutex
	busy     map[int64]int
	attempts map[int64]int
	waiting  map[int64]int
	maxSeen  map[int64]int
}

func newSelectionTestConcurrency() *selectionTestConcurrency {
	return &selectionTestConcurrency{mockConcurrencyCache: &mockConcurrencyCache{}, busy: map[int64]int{},
		attempts: map[int64]int{}, waiting: map[int64]int{}, maxSeen: map[int64]int{}}
}

func (c *selectionTestConcurrency) AcquireAccountSlot(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts[accountID]++
	if c.busy[accountID] != 0 {
		if c.busy[accountID] > 0 {
			c.busy[accountID]--
		}
		return false, nil
	}
	return true, nil
}

func (c *selectionTestConcurrency) IncrementAccountWaitCount(_ context.Context, accountID int64, maxWait int) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiting[accountID] >= maxWait {
		return false, nil
	}
	c.waiting[accountID]++
	c.maxSeen[accountID] = max(c.maxSeen[accountID], c.waiting[accountID])
	return true, nil
}

func (c *selectionTestConcurrency) DecrementAccountWaitCount(_ context.Context, accountID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiting[accountID]--
	return nil
}

// accountIn returns the account with id from a listing the test repository
// serves.
func accountIn(accounts []Account, id int64) *Account {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

type gatewaySelectionTestEnv struct {
	gateway *GatewayService
	cache   *selectionTestCache
	conc    *selectionTestConcurrency
	repo    *mockAccountRepoForPlatform
}

// selectionTestRule is a member's tier and weight in a test. Share groups take the
// tier from the account priority and the weight from the load factor, so the
// test environments write rules into the accounts.
type selectionTestRule struct {
	AccountID    int64
	Tier, Weight int
}

// selectionTestSetup is a group policy with the account settings of a test.
type selectionTestSetup struct {
	rules []selectionTestRule
}

func selectionTestSetupWithRules(rules ...selectionTestRule) selectionTestSetup {
	return selectionTestSetup{rules: rules}
}

// apply writes the rule of acc, if any, into its priority and load factor.
func (s selectionTestSetup) apply(acc *Account) {
	for _, rule := range s.rules {
		if rule.AccountID == acc.ID {
			weight := rule.Weight
			acc.Priority, acc.LoadFactor = rule.Tier, &weight
		}
	}
}

func newGatewaySelectionTestEnv(t testing.TB, setup selectionTestSetup, accounts ...Account) *gatewaySelectionTestEnv {
	t.Helper()
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{}}
	for _, acc := range accounts {
		setup.apply(&acc)
		if acc.Platform == "" {
			acc.Platform = PlatformAnthropic
		}
		if acc.Type == "" {
			acc.Type = AccountTypeAPIKey
		}
		if acc.Status == "" {
			acc.Status = StatusActive
		}
		acc.Schedulable = true
		if acc.Concurrency == 0 {
			acc.Concurrency = 5
		}
		acc.AccountGroups = []AccountGroup{{GroupID: selectionTestGroupID}}
		repo.accounts = append(repo.accounts, acc)
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	cache := newSelectionTestCache()
	conc := newSelectionTestConcurrency()
	gateway := &GatewayService{
		accountRepo: repo,
		groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{
			selectionTestGroupID: {ID: selectionTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(conc),
	}
	return &gatewaySelectionTestEnv{gateway: gateway, cache: cache, conc: conc, repo: repo}
}

func (e *gatewaySelectionTestEnv) selectAccount(t *testing.T, ctx context.Context, sessionHash string, excluded map[int64]struct{}) *AccountSelectionResult {
	t.Helper()
	gid := selectionTestGroupID
	result, err := e.gateway.SelectAccountWithLoadAwareness(ctx, &gid, sessionHash, "claude-sonnet-4-5", excluded, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	return result
}

type openAISelectionTestEnv struct {
	svc   *OpenAIGatewayService
	cache *selectionTestCache
	conc  *selectionTestConcurrency
	repo  schedulerTestOpenAIAccountRepo
}

func newOpenAISelectionTestEnv(t *testing.T, setup selectionTestSetup, ids ...int64) *openAISelectionTestEnv {
	t.Helper()
	accounts := make([]Account, 0, len(ids))
	for _, id := range ids {
		acc := Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
			Schedulable: true, Concurrency: 5, AccountGroups: []AccountGroup{{GroupID: selectionTestGroupID}}}
		setup.apply(&acc)
		accounts = append(accounts, acc)
	}
	repo := schedulerTestOpenAIAccountRepo{accounts: accounts}
	cache := newSelectionTestCache()
	conc := newSelectionTestConcurrency()
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: cfg,
		concurrencyService: NewConcurrencyService(conc)}
	return &openAISelectionTestEnv{svc: svc, cache: cache, conc: conc, repo: repo}
}

func (e *openAISelectionTestEnv) selectAccount(t *testing.T, ctx context.Context, previousResponseID, sessionHash string, excluded map[int64]struct{}) (*AccountSelectionResult, OpenAIAccountScheduleDecision) {
	t.Helper()
	gid := selectionTestGroupID
	result, decision, err := e.svc.SelectAccountWithScheduler(ctx, &gid, previousResponseID, sessionHash, "gpt-5.1", excluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	return result, decision
}

func (e *openAISelectionTestEnv) account(id int64) *Account {
	for i := range e.repo.accounts {
		if e.repo.accounts[i].ID == id {
			return &e.repo.accounts[i]
		}
	}
	return nil
}

// openAISelectionTarget places a fresh session in a separate environment and
// returns its first choice.
func openAISelectionTarget(t *testing.T, session string) (target, other int64) {
	t.Helper()
	probe := newOpenAISelectionTestEnv(t, twoAccountSelectionSetup(), 1, 2)
	result, _ := probe.selectAccount(t, context.Background(), "", session, nil)
	return result.Account.ID, 3 - result.Account.ID
}

func twoAccountSelectionSetup() selectionTestSetup {
	return selectionTestSetupWithRules(selectionTestRule{AccountID: 1, Tier: 1, Weight: 3}, selectionTestRule{AccountID: 2, Tier: 1, Weight: 1})
}
