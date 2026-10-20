//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

const shareTestGroupID = int64(7)

// shareTestCache adds the TTL read and SET NX of the Redis gateway cache and
// records every binding write.
type shareTestCache struct {
	*mockGatewayCacheForPlatform
	mu        sync.Mutex
	remaining map[string]time.Duration
	ttls      map[string]time.Duration
	refreshed []string
	replaced  []string
	created   []string
}

func newShareTestCache() *shareTestCache {
	return &shareTestCache{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		remaining: map[string]time.Duration{}, ttls: map[string]time.Duration{}}
}

func (c *shareTestCache) bind(key string, accountID int64, remaining time.Duration) {
	c.sessionBindings[key] = accountID
	c.remaining[key] = remaining
}

func (c *shareTestCache) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.sessionBindings[key]; ok {
		return id, nil
	}
	return 0, ErrStickySessionNotFound
}

func (c *shareTestCache) GetSessionAccountIDWithTTL(_ context.Context, _ int64, key string) (int64, time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.sessionBindings[key]
	if !ok {
		return 0, 0, ErrStickySessionNotFound
	}
	return id, c.remaining[key], nil
}

func (c *shareTestCache) SetSessionAccountID(_ context.Context, _ int64, key string, accountID int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionBindings[key], c.ttls[key] = accountID, ttl
	c.replaced = append(c.replaced, key)
	return nil
}

func (c *shareTestCache) SetSessionAccountIDIfAbsent(_ context.Context, _ int64, key string, accountID int64, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sessionBindings[key]; ok {
		return false, nil
	}
	c.sessionBindings[key], c.ttls[key] = accountID, ttl
	c.created = append(c.created, key)
	return true, nil
}

func (c *shareTestCache) RefreshSessionTTL(_ context.Context, _ int64, key string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttls[key] = ttl
	c.refreshed = append(c.refreshed, key)
	return nil
}

// shareTestConcurrency rejects the first busy[id] slot requests of an
// account and tracks the wait queue.
type shareTestConcurrency struct {
	*mockConcurrencyCache
	mu       sync.Mutex
	busy     map[int64]int
	attempts map[int64]int
	waiting  map[int64]int
	maxSeen  map[int64]int
}

func newShareTestConcurrency() *shareTestConcurrency {
	return &shareTestConcurrency{mockConcurrencyCache: &mockConcurrencyCache{}, busy: map[int64]int{},
		attempts: map[int64]int{}, waiting: map[int64]int{}, maxSeen: map[int64]int{}}
}

func (c *shareTestConcurrency) AcquireAccountSlot(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
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

func (c *shareTestConcurrency) IncrementAccountWaitCount(_ context.Context, accountID int64, maxWait int) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiting[accountID] >= maxWait {
		return false, nil
	}
	c.waiting[accountID]++
	c.maxSeen[accountID] = max(c.maxSeen[accountID], c.waiting[accountID])
	return true, nil
}

func (c *shareTestConcurrency) DecrementAccountWaitCount(_ context.Context, accountID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiting[accountID]--
	return nil
}

type shareTestEnv struct {
	gateway *GatewayService
	share   *ShareSchedulingService
	cache   *shareTestCache
	conc    *shareTestConcurrency
	repo    *mockAccountRepoForPlatform
}

// shareRule is a member's tier and weight in a test. Share groups take the
// tier from the account priority and the weight from the load factor, so the
// test environments write rules into the accounts.
type shareRule struct {
	AccountID    int64
	Tier, Weight int
}

// shareTestSetup is a group policy with the account settings of a test.
type shareTestSetup struct {
	policy sharesched.Policy
	rules  []shareRule
}

func shareTestPolicy(scheduler sharesched.Scheduler, rules ...shareRule) shareTestSetup {
	p := sharesched.DefaultPolicy()
	p.Scheduler = scheduler
	return shareTestSetup{policy: p, rules: rules}
}

// apply writes the rule of acc, if any, into its priority and load factor.
func (s shareTestSetup) apply(acc *Account) {
	for _, rule := range s.rules {
		if rule.AccountID == acc.ID {
			weight := rule.Weight
			acc.Priority, acc.LoadFactor = rule.Tier, &weight
		}
	}
}

func newShareTestEnv(t testing.TB, setup shareTestSetup, accounts ...Account) *shareTestEnv {
	t.Helper()
	policy := setup.policy
	require.NoError(t, policy.Validate())
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
		acc.AccountGroups = []AccountGroup{{GroupID: shareTestGroupID}}
		repo.accounts = append(repo.accounts, acc)
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	cache := newShareTestCache()
	conc := newShareTestConcurrency()
	share := NewShareSchedulingService(nil, nil, nil)
	share.attributionRate = 1
	share.state.Store(&shareSchedulingState{
		settings: ShareSchedulingSettings{Enabled: true},
		groups:   map[int64]*shareGroupPolicy{shareTestGroupID: {groupID: shareTestGroupID, version: 3, policy: policy}},
	})
	gateway := &GatewayService{
		accountRepo: repo,
		groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{
			shareTestGroupID: {ID: shareTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(conc),
		shareScheduling:    share,
	}
	return &shareTestEnv{gateway: gateway, share: share, cache: cache, conc: conc, repo: repo}
}

func (e *shareTestEnv) selectAccount(t *testing.T, ctx context.Context, sessionHash string, excluded map[int64]struct{}) *AccountSelectionResult {
	t.Helper()
	gid := shareTestGroupID
	result, err := e.gateway.SelectAccountWithLoadAwareness(ctx, &gid, sessionHash, "claude-sonnet-4-5", excluded, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	return result
}

func (e *shareTestEnv) counter(field string) int64 {
	e.share.stats.mu.Lock()
	defer e.share.stats.mu.Unlock()
	var n int64
	for key, v := range e.share.stats.counters {
		if key.Field == field {
			n += v
		}
	}
	return n
}

// cacheableContext marks the request as long enough to have a cache worth
// protecting: 40 KB is about 10k tokens.
func cacheableContext() context.Context {
	return withShareRequestProfile(context.Background(), make([]byte, 40_000))
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

func twoAccountPolicy() shareTestSetup {
	return shareTestPolicy(sharesched.SchedulerShare,
		shareRule{AccountID: 1, Tier: 1, Weight: 3},
		shareRule{AccountID: 2, Tier: 1, Weight: 1})
}

// targetFor places a fresh session and returns its HRW first choice.
func (e *shareTestEnv) targetFor(t *testing.T, key string) (target, other int64) {
	t.Helper()
	probe := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target = probe.selectAccount(t, context.Background(), key, nil).Account.ID
	other = 3 - target
	return target, other
}

func TestShareSchedulingNewSessionsFollowWeights(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	served := map[int64]int{}
	const sessions = 2000
	for i := 0; i < sessions; i++ {
		key := fmt.Sprintf("session-%d", i)
		result := env.selectAccount(t, context.Background(), key, nil)
		require.True(t, result.Acquired)
		served[result.Account.ID]++
		require.Equal(t, result.Account.ID, env.cache.sessionBindings[key], "new sessions bind to the serving account")
		require.Equal(t, time.Hour, env.cache.ttls[key], "bindings use affinity.ttl_seconds")
	}
	require.Len(t, env.cache.created, sessions, "new sessions bind with SET NX")
	require.Empty(t, env.cache.replaced)
	share := float64(served[1]) / sessions
	require.InDelta(t, 0.75, share, 0.03, "weights 3:1 give account 1 three quarters of new sessions")
	require.Equal(t, int64(sessions), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonNewSession)))

	again := env.selectAccount(t, context.Background(), "session-0", nil)
	require.Equal(t, env.cache.sessionBindings["session-0"], again.Account.ID, "the next turn stays on the bound account")
}

func TestShareSchedulingAffinityHitRefreshesWarmBinding(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "warm")
	env.cache.bind("warm", other, time.Hour-30*time.Second)

	result := env.selectAccount(t, cacheableContext(), "warm", nil)
	require.Equal(t, other, result.Account.ID, "a warm session stays on its account even when another is ideal (target %d)", target)
	require.Equal(t, []string{"warm"}, env.cache.refreshed)
	require.Empty(t, env.cache.replaced)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonAffinityHit)))
	require.Zero(t, env.counter(shareStatsHotMigrationField))
}

func TestShareSchedulingRealignsColdSessions(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "cold")
	// Idle for ten minutes: the five-minute Anthropic cache has expired.
	env.cache.bind("cold", other, time.Hour-10*time.Minute)

	result := env.selectAccount(t, cacheableContext(), "cold", nil)
	require.Equal(t, target, result.Account.ID, "a cold session moves to its ideal account for free")
	require.Equal(t, target, env.cache.sessionBindings["cold"])
	require.Equal(t, []string{"cold"}, env.cache.replaced)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonRealignCold)))
	require.Zero(t, env.counter(shareStatsHotMigrationField), "realignment never moves a warm cache")
}

func TestShareSchedulingSpillsAndRebindsWhenBoundIsSaturated(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "busy")
	env.cache.bind("busy", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	// A short prompt has no cache worth waiting for.
	result := env.selectAccount(t, withShareRequestProfile(context.Background(), make([]byte, 400)), "busy", nil)
	require.Equal(t, other, result.Account.ID)
	require.True(t, result.Acquired)
	require.Equal(t, other, env.cache.sessionBindings["busy"], "a spill rebinds: the cache moves with the session")
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSpillRebind)))
	require.Equal(t, int64(1), env.counter(shareStatsIdealSkipPrefix+string(sharesched.ReasonSaturated)))
}

func TestShareSchedulingWaitsBoundedlyForWarmBoundAccount(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, _ := env.targetFor(t, "wait")
	env.cache.bind("wait", target, time.Hour-30*time.Second)
	env.conc.busy[target] = 2

	start := time.Now()
	result := env.selectAccount(t, cacheableContext(), "wait", nil)
	require.Equal(t, target, result.Account.ID, "a long warm prompt waits for its own account")
	require.True(t, result.Acquired)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 1, env.conc.maxSeen[target], "the wait is counted in the account's queue")
	require.Zero(t, env.conc.waiting[target], "the queue slot is released")
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonAffinityWait)))
	require.Equal(t, int64(1), env.counter(shareStatsWaitHitField))
}

func TestShareSchedulingWaitRespectsQueueLimit(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "queue")
	env.cache.bind("queue", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1
	env.conc.waiting[target] = 3

	start := time.Now()
	result := env.selectAccount(t, cacheableContext(), "queue", nil)
	require.Equal(t, other, result.Account.ID, "a full queue spills immediately")
	require.Less(t, time.Since(start), 200*time.Millisecond)
}

func TestShareSchedulingCanceledWaitReleasesQueueAndStops(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "gone")
	env.cache.bind("gone", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1
	ctx, cancel := context.WithCancel(cacheableContext())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	result := env.selectAccount(t, ctx, "gone", nil)
	require.Less(t, time.Since(start), 500*time.Millisecond, "a client that leaves ends the wait")
	require.False(t, result.Acquired)
	require.Equal(t, 1, env.conc.maxSeen[target])
	require.Zero(t, env.conc.waiting[target], "the queue slot is released")
	require.Zero(t, env.conc.attempts[other], "no slot is taken for a request that is gone")
	require.Zero(t, env.counter(fmt.Sprintf("%s%d:%s", shareStatsSkipPrefix, target, sharesched.ReasonSaturated)),
		"an abandoned wait is not counted as saturation")
}

func TestShareSchedulingShortSessionsDoNotWait(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "short")
	env.cache.bind("short", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	// A tiny prompt has no cache worth protecting.
	ctx := withShareRequestProfile(context.Background(), make([]byte, 400))
	result := env.selectAccount(t, ctx, "short", nil)
	require.Equal(t, other, result.Account.ID)
	require.Zero(t, env.conc.maxSeen[target])
}

func TestShareSchedulingSoftSpillKeepsBindingThroughShortOutage(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "outage")
	resetAt := time.Now().Add(time.Minute)
	env.repo.accountsByID[target].RateLimitResetAt = &resetAt
	for i := range env.repo.accounts {
		if env.repo.accounts[i].ID == target {
			env.repo.accounts[i].RateLimitResetAt = &resetAt
		}
	}
	env.cache.bind("outage", target, time.Hour-30*time.Second)

	result := env.selectAccount(t, cacheableContext(), "outage", nil)
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, target, env.cache.sessionBindings["outage"], "the binding survives a one-minute rate limit")
	require.Empty(t, env.cache.replaced)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSoftSpill)))

	longReset := time.Now().Add(20 * time.Minute)
	env.repo.accountsByID[target].RateLimitResetAt = &longReset
	result = env.selectAccount(t, cacheableContext(), "outage", nil)
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, other, env.cache.sessionBindings["outage"], "a long outage rebinds")
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonRebindUnavailable)))
}

// TestShareSchedulingHardTemporaryStateRebinds: soft and hard states follow
// gateway resilience (design §4). A temporary state whose reason is empty or
// not a soft prefix is hard, however short, and the session moves at once.
//
// With the resilience master switch off the 0.3.0 rule applies: any state
// that ends within 300 seconds keeps the binding, a balance pause included.
func TestShareSchedulingHardTemporaryStateRebinds(t *testing.T) {
	for _, tc := range []struct {
		reason string
		off    bool
		soft   bool
	}{
		{"", false, false},
		{"administrator rule: keyword matched", false, false},
		{"balance:exhausted: HTTP 402", false, false},
		{"transient: HTTP 502", false, true},
		{"", true, true},
		{"administrator rule: keyword matched", true, true},
		{"balance:exhausted: HTTP 402", true, true},
	} {
		t.Run(fmt.Sprintf("%s/off=%v", tc.reason, tc.off), func(t *testing.T) {
			if tc.off {
				orig := *resilience.Current()
				off := resilience.Defaults()
				off.Enabled = false
				resilience.Publish(off)
				t.Cleanup(func() { resilience.Publish(orig) })
			}
			env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
			target, other := env.targetFor(t, "temp")
			until := time.Now().Add(time.Minute)
			for _, acc := range []*Account{env.repo.accountsByID[target], accountIn(env.repo.accounts, target)} {
				acc.TempUnschedulableUntil, acc.TempUnschedulableReason = &until, tc.reason
			}
			env.cache.bind("temp", target, time.Hour-30*time.Second)

			result := env.selectAccount(t, cacheableContext(), "temp", nil)
			require.Equal(t, other, result.Account.ID)
			if tc.soft {
				require.Equal(t, target, env.cache.sessionBindings["temp"], "a soft penalty keeps the binding")
				require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSoftSpill)))
			} else {
				require.Equal(t, other, env.cache.sessionBindings["temp"], "a hard state moves the session")
				require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonRebindUnavailable)))
			}
		})
	}
}

// TestShareSchedulingSkipsAccountsAtFullLoad: new placements and spills skip
// accounts whose load rate is 100 or more, as the original selection does
// (design §5); a bound session is judged by its slots only.
func TestShareSchedulingSkipsAccountsAtFullLoad(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	// Load factor 3 with three requests in flight: load rate 100.
	env.conc.mockConcurrencyCache.loadMap = map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 3, LoadRate: 100}}
	for i := 0; i < 40; i++ {
		result := env.selectAccount(t, context.Background(), fmt.Sprintf("full-%d", i), nil)
		require.Equal(t, int64(2), result.Account.ID, "new sessions go to the account with room")
	}
	require.Positive(t, env.counter(fmt.Sprintf("%s%d:%s", shareStatsSkipPrefix, 1, sharesched.ReasonSaturated)))
	require.Zero(t, env.conc.attempts[1], "a full account is not even tried")

	env.cache.bind("bound", 1, time.Hour-30*time.Second)
	result := env.selectAccount(t, cacheableContext(), "bound", nil)
	require.Equal(t, int64(1), result.Account.ID, "a bound session keeps its account while it has a free slot")
}

func TestShareSchedulingFailoverMovesToNextAccount(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "failover")
	env.cache.bind("failover", target, time.Hour-30*time.Second)

	result := env.selectAccount(t, cacheableContext(), "failover", map[int64]struct{}{target: {}})
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, other, env.cache.sessionBindings["failover"])
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonFailoverNext)))
}

func TestShareSchedulingTierOverflow(t *testing.T) {
	// Tiers are account priorities.
	env := newShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShare),
		Account{ID: 1, Priority: 1}, Account{ID: 2, Priority: 2}, Account{ID: 3, Priority: 3})
	for i := 0; i < 50; i++ {
		result := env.selectAccount(t, context.Background(), fmt.Sprintf("tier-%d", i), nil)
		require.Equal(t, int64(1), result.Account.ID, "the first priority takes every new session")
	}
	env.conc.busy[1] = -1
	result := env.selectAccount(t, context.Background(), "overflow", nil)
	require.Equal(t, int64(2), result.Account.ID, "saturation spills to the next priority")
}

func TestShareSchedulingFallbackWaitDefersBinding(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	env.conc.busy[1], env.conc.busy[2] = -1, -1

	result := env.selectAccount(t, context.Background(), "queued", nil)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.Equal(t, 30*time.Second, result.WaitPlan.Timeout)
	require.NotContains(t, env.cache.sessionBindings, "queued", "nothing binds before the wait succeeds")
	require.NotNil(t, result.shareBinding)

	gid := shareTestGroupID
	ctx := ContextWithSelectionProfitGate(context.Background(), result)
	require.NoError(t, env.gateway.BindStickySessionAfterProfitAdmission(ctx, &gid, "queued", result.Account.ID))
	require.Equal(t, result.Account.ID, env.cache.sessionBindings["queued"])
	require.Equal(t, []string{"queued"}, env.cache.created, "the deferred write keeps SET NX semantics")
}

func TestShareSchedulingModelRoutingRestrictsThenFallsBack(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	group := env.gateway.groupRepo.(*mockGroupRepoForGateway).groups[shareTestGroupID]
	group.ModelRoutingEnabled = true
	group.ModelRouting = map[string][]int64{"claude-sonnet-4-5": {2}}

	result := env.selectAccount(t, context.Background(), "routed", nil)
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonModelRouting)))

	env.conc.busy[2] = -1
	result = env.selectAccount(t, context.Background(), "routed-busy", nil)
	require.Equal(t, int64(1), result.Account.ID)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonModelRoutingFallback)))
}

func TestShareSchedulingDisabledKeepsOriginalScheduler(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	state := env.share.state.Load()
	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: false}, groups: state.groups})

	result := env.selectAccount(t, context.Background(), "original", nil)
	require.NotNil(t, result.Account)
	require.Empty(t, env.cache.created, "the original scheduler binds with a plain SET")
	require.Zero(t, env.counter(shareStatsRequestsField))
}

// TestShareSchedulingStoredShadowRunsOriginal: the retired shadow
// scheduler runs the original selection and compares nothing (design §3.3).
func TestShareSchedulingStoredShadowRunsOriginal(t *testing.T) {
	env := newShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShadow,
		shareRule{AccountID: 1, Tier: 1, Weight: 3},
		shareRule{AccountID: 2, Tier: 1, Weight: 1}), Account{ID: 1}, Account{ID: 2})

	result := env.selectAccount(t, context.Background(), "shadow", nil)
	require.NotNil(t, result.Account)
	require.Empty(t, env.cache.created, "the original scheduler binds with a plain SET")
	require.Zero(t, env.counter(shareStatsRequestsField), "no share decision is made or compared")
}

func TestShareSchedulingBindStickySessionUsesBindingTTL(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	gid := shareTestGroupID
	require.NoError(t, env.gateway.BindStickySession(context.Background(), &gid, "ttl", 1))
	require.Equal(t, sharesched.BindingTTL, env.cache.ttls["ttl"], "idle time is derived from the binding TTL, so every write must use it")

	other := int64(99)
	require.NoError(t, env.gateway.BindStickySession(context.Background(), &other, "ttl-other", 1))
	require.Equal(t, stickySessionTTL, env.cache.ttls["ttl-other"])
}

func TestShareSchedulingStatisticsNeverCarryRawSessionKeys(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	env.selectAccount(t, context.Background(), "secret-session-id", nil)
	batch := env.share.stats.drain()
	require.NotEmpty(t, batch.Counters)
	for counter := range batch.Counters {
		require.NotContains(t, counter.Field, "secret-session-id")
	}
}

func TestShareSessionHashAnchorsConversations(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	gid := shareTestGroupID
	parse := func(t *testing.T, body string, apiKeyID int64) *ParsedRequest {
		t.Helper()
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
		require.NoError(t, err)
		parsed.GroupID = &gid
		parsed.SessionContext = &SessionContext{ClientIP: "1.2.3.4", UserAgent: "x", APIKeyID: apiKeyID}
		return parsed
	}
	turn1 := `{"model":"m","system":"you are helpful","messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`
	turn2 := `{"model":"m","system":"you are helpful","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"text","text":"next","cache_control":{"type":"ephemeral"}}]}]}`

	h1 := env.gateway.GenerateSessionHash(parse(t, turn1, 11))
	h2 := env.gateway.GenerateSessionHash(parse(t, turn2, 11))
	require.True(t, strings.HasPrefix(h1, shareAnchorPrefix))
	require.Equal(t, h1, h2, "moving cache breakpoints does not change the anchor")
	require.NotEqual(t, h1, env.gateway.GenerateSessionHash(parse(t, turn1, 12)), "anchors never cross API keys")

	withSession := `{"model":"m","metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_6f1c3b0e-9d7e-4c2b-8f4a-1b2c3d4e5f60"},"messages":[{"role":"user","content":"hello"}]}`
	require.False(t, strings.HasPrefix(env.gateway.GenerateSessionHash(parse(t, withSession, 11)), shareAnchorPrefix), "explicit session ids keep priority")

	// The retired prefix key keeps sessions as auto does.
	state := env.share.state.Load()
	prefix, err := sharesched.DecodePolicy([]byte(`{"schema":1,"scheduler":"share","affinity":{"key":"prefix"}}`))
	require.NoError(t, err)
	env.share.state.Store(&shareSchedulingState{settings: state.settings, groups: map[int64]*shareGroupPolicy{
		shareTestGroupID: {groupID: shareTestGroupID, version: 4, policy: prefix}}})
	other := `{"model":"m","system":"you are helpful","messages":[{"role":"user","content":"a different task"}]}`
	p1 := env.gateway.GenerateSessionHash(parse(t, turn1, 11))
	require.Equal(t, h1, p1)
	require.NotEqual(t, p1, env.gateway.GenerateSessionHash(parse(t, other, 11)), "requests sharing a system prompt are separate conversations")

	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{}, groups: state.groups})
	original := env.gateway.GenerateSessionHash(parse(t, turn1, 11))
	require.False(t, strings.HasPrefix(original, shareAnchorPrefix), "original groups keep the original hash")
}

func TestShareAffinityKeyFollowsSessionKeeping(t *testing.T) {
	p := sharesched.DefaultPolicy()
	key, source := shareAffinityKey(&p, "anchor:x")
	require.Equal(t, "anchor:x", key)
	require.Equal(t, sharesched.KeyAnchor, source)
	_, source = shareAffinityKey(&p, "uuid-session")
	require.Equal(t, sharesched.KeySession, source)

	for _, body := range []string{`{"schema":1,"scheduler":"share","affinity":{"key":"session"}}`, `{"schema":1,"scheduler":"share","affinity":{"key":"prefix"}}`} {
		retired, err := sharesched.DecodePolicy([]byte(body))
		require.NoError(t, err)
		key, source = shareAffinityKey(&retired, "anchor:x")
		require.Equal(t, "anchor:x", key, "retired keys keep sessions as auto does")
		require.Equal(t, sharesched.KeyAnchor, source)
	}

	p.Affinity.Key = sharesched.AffinityKeyNone
	key, source = shareAffinityKey(&p, "uuid-session")
	require.Empty(t, key, "session keeping off")
	require.Equal(t, sharesched.KeyNone, source)
	p.Affinity.Key, p.Affinity.Enabled = sharesched.AffinityKeyAuto, false
	key, _ = shareAffinityKey(&p, "uuid-session")
	require.Empty(t, key)
}

func TestReadShareBindingIdleTime(t *testing.T) {
	cache := newShareTestCache()
	cache.bind("k", 5, 50*time.Minute)
	binding, err := readShareBinding(context.Background(), cache, 1, "k", time.Hour)
	require.NoError(t, err)
	require.Equal(t, sharesched.Binding{AccountID: 5, Idle: 10 * time.Minute}, binding)

	cache.bind("longer", 5, 2*time.Hour)
	binding, err = readShareBinding(context.Background(), cache, 1, "longer", time.Hour)
	require.NoError(t, err)
	require.Zero(t, binding.Idle, "a binding written with a longer TTL counts as fresh")

	binding, err = readShareBinding(context.Background(), cache, 1, "missing", time.Hour)
	require.NoError(t, err)
	require.Zero(t, binding.AccountID)
}

func TestShareRequestProfileDetectsOneHourCache(t *testing.T) {
	ctx := withShareRequestProfile(context.Background(), []byte(`{"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`))
	profile := shareRequestProfileFromContext(ctx)
	require.True(t, profile.cacheOneHour)
	require.Equal(t, time.Hour, shareCacheProfile(ctx, PlatformAnthropic, "claude-sonnet-4-5").TTL)
	plain := withShareRequestProfile(context.Background(), []byte(`{"messages":[]}`))
	require.Equal(t, 5*time.Minute, shareCacheProfile(plain, PlatformAnthropic, "claude-sonnet-4-5").TTL)
	require.Equal(t, 30*time.Minute, shareCacheProfile(plain, PlatformOpenAI, "gpt-5.6").TTL)
}
