//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func shareTestPolicy(scheduler sharesched.Scheduler, rules ...sharesched.AccountRule) sharesched.Policy {
	p := sharesched.DefaultPolicy()
	p.Scheduler = scheduler
	p.Accounts = rules
	return p
}

func newShareTestEnv(t testing.TB, policy sharesched.Policy, accounts ...Account) *shareTestEnv {
	t.Helper()
	require.NoError(t, policy.Validate())
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{}}
	for _, acc := range accounts {
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
	share.state.Store(&shareSchedulingState{
		settings: ShareSchedulingSettings{Enabled: true, CacheAttribution: true, AttributionSampleRate: 1, DecisionSamples: true},
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

func twoAccountPolicy() sharesched.Policy {
	return shareTestPolicy(sharesched.SchedulerShare,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 3},
		sharesched.AccountRule{AccountID: 2, Tier: 1, Weight: 1})
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
	policy := twoAccountPolicy()
	policy.Affinity.Wait.MaxWaiting = 0
	env := newShareTestEnv(t, policy, Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "busy")
	env.cache.bind("busy", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	result := env.selectAccount(t, cacheableContext(), "busy", nil)
	require.Equal(t, other, result.Account.ID)
	require.True(t, result.Acquired)
	require.Equal(t, other, env.cache.sessionBindings["busy"], "on_spill=rebind moves the cache with the session")
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSpillRebind)))
	require.Equal(t, int64(1), env.counter(shareStatsIdealSkipPrefix+string(sharesched.ReasonSaturated)))
}

func TestShareSchedulingKeepPolicyDoesNotRebindOnSpill(t *testing.T) {
	policy := twoAccountPolicy()
	policy.Affinity.Wait.MaxWaiting = 0
	policy.Affinity.OnSpill = sharesched.OnSpillKeep
	env := newShareTestEnv(t, policy, Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "keep")
	env.cache.bind("keep", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	result := env.selectAccount(t, cacheableContext(), "keep", nil)
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, target, env.cache.sessionBindings["keep"])
	require.Empty(t, env.cache.replaced)
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

func TestShareSchedulingFailoverMovesToNextAccount(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "failover")
	env.cache.bind("failover", target, time.Hour-30*time.Second)

	result := env.selectAccount(t, cacheableContext(), "failover", map[int64]struct{}{target: {}})
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, other, env.cache.sessionBindings["failover"])
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonFailoverNext)))
}

func TestShareSchedulingTierOverflowAndDrain(t *testing.T) {
	policy := shareTestPolicy(sharesched.SchedulerShare,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 1},
		sharesched.AccountRule{AccountID: 2, Tier: 2, Weight: 1},
		sharesched.AccountRule{AccountID: 3, Tier: 1, Weight: 1, Drain: true})
	env := newShareTestEnv(t, policy, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})
	for i := 0; i < 50; i++ {
		result := env.selectAccount(t, context.Background(), fmt.Sprintf("tier-%d", i), nil)
		require.Equal(t, int64(1), result.Account.ID, "the first tier takes every new session; drained accounts take none")
	}
	env.conc.busy[1] = -1
	result := env.selectAccount(t, context.Background(), "overflow", nil)
	require.Equal(t, int64(2), result.Account.ID, "saturation spills to the next tier, never to a drained account")
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
	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: false, AttributionSampleRate: 1}, groups: state.groups})

	result := env.selectAccount(t, context.Background(), "original", nil)
	require.NotNil(t, result.Account)
	require.Empty(t, env.cache.created, "the original scheduler binds with a plain SET")
	require.Zero(t, env.counter(shareStatsRequestsField))
}

func TestShareSchedulingShadowComparesWithoutRouting(t *testing.T) {
	env := newShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShadow,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 3},
		sharesched.AccountRule{AccountID: 2, Tier: 1, Weight: 1}), Account{ID: 1}, Account{ID: 2})

	result := env.selectAccount(t, context.Background(), "shadow", nil)
	require.NotNil(t, result.Account)
	require.Empty(t, env.cache.created, "shadow mode never writes share bindings")
	require.Eventually(t, func() bool { return env.counter(shareStatsShadowField) == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Zero(t, env.counter(shareStatsRequestsField), "shadow decisions are not served decisions")
}

func TestShareSchedulingBindStickySessionUsesPolicyTTL(t *testing.T) {
	policy := twoAccountPolicy()
	policy.Affinity.TTLSeconds = 1800
	env := newShareTestEnv(t, policy, Account{ID: 1}, Account{ID: 2})
	gid := shareTestGroupID
	require.NoError(t, env.gateway.BindStickySession(context.Background(), &gid, "ttl", 1))
	require.Equal(t, 30*time.Minute, env.cache.ttls["ttl"], "idle time is derived from the policy TTL, so every write must use it")

	other := int64(99)
	require.NoError(t, env.gateway.BindStickySession(context.Background(), &other, "ttl-other", 1))
	require.Equal(t, stickySessionTTL, env.cache.ttls["ttl-other"])
}

func TestShareSchedulingDecisionSamplesNeverCarryRawSessionKeys(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	env.selectAccount(t, context.Background(), "secret-session-id", nil)
	batch := env.share.stats.drain()
	require.Len(t, batch.Decisions[shareTestGroupID], 1)
	sample := string(batch.Decisions[shareTestGroupID][0])
	require.NotContains(t, sample, "secret-session-id")
	require.Contains(t, sample, `"reason":"new_session"`)
	require.Contains(t, sample, `"version":3`)
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

	state := env.share.state.Load()
	prefix := state.groups[shareTestGroupID].policy
	prefix.Affinity.Key = sharesched.AffinityKeyPrefix
	env.share.state.Store(&shareSchedulingState{settings: state.settings, groups: map[int64]*shareGroupPolicy{
		shareTestGroupID: {groupID: shareTestGroupID, version: 4, policy: prefix}}})
	other := `{"model":"m","system":"you are helpful","messages":[{"role":"user","content":"a different task"}]}`
	p1 := env.gateway.GenerateSessionHash(parse(t, turn1, 11))
	require.True(t, strings.HasPrefix(p1, sharePrefixKeyPrefix))
	require.Equal(t, p1, env.gateway.GenerateSessionHash(parse(t, other, 11)), "prefix mode groups requests that share a system prompt")

	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{AttributionSampleRate: 1}, groups: state.groups})
	original := env.gateway.GenerateSessionHash(parse(t, turn1, 11))
	require.False(t, strings.HasPrefix(original, shareAnchorPrefix), "original groups keep the original hash")
}

func TestShareAffinityKeyModes(t *testing.T) {
	p := sharesched.DefaultPolicy()
	key, source := shareAffinityKey(&p, "anchor:x")
	require.Equal(t, "anchor:x", key)
	require.Equal(t, sharesched.KeyAnchor, source)
	_, source = shareAffinityKey(&p, "uuid-session")
	require.Equal(t, sharesched.KeySession, source)
	_, source = shareAffinityKey(&p, "prefix:x")
	require.Equal(t, sharesched.KeyPrefix, source)

	p.Affinity.Key = sharesched.AffinityKeySession
	key, source = shareAffinityKey(&p, "anchor:x")
	require.Empty(t, key, "session mode only trusts explicit session ids")
	require.Equal(t, sharesched.KeyNone, source)

	p.Affinity.Key = sharesched.AffinityKeyAuto
	p.Affinity.Enabled = false
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
	p := sharesched.DefaultPolicy()
	require.Equal(t, time.Hour, shareCacheProfile(ctx, &p, PlatformAnthropic, "claude-sonnet-4-5").TTL)
	plain := withShareRequestProfile(context.Background(), []byte(`{"messages":[]}`))
	require.Equal(t, 5*time.Minute, shareCacheProfile(plain, &p, PlatformAnthropic, "claude-sonnet-4-5").TTL)
	require.Equal(t, 30*time.Minute, shareCacheProfile(plain, &p, PlatformOpenAI, "gpt-5.6").TTL)
}

func TestShareCorrectionFactors(t *testing.T) {
	p := shareTestPolicy(sharesched.SchedulerShare,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 1},
		sharesched.AccountRule{AccountID: 2, Tier: 1, Weight: 1})
	p.Balance.Unit = sharesched.UnitRequests
	p.Balance.Correction = sharesched.CorrectionGentle
	p.Balance.MinVolume = 10
	factors := shareCorrectionFactors(&p, map[int64]ShareSchedulingUsage{1: {Requests: 90}, 2: {Requests: 10}})
	require.Greater(t, factors[2], 1.0, "an under-served account gets a boost")
	require.Less(t, factors[1], 1.0)
	require.Empty(t, shareCorrectionFactors(&p, map[int64]ShareSchedulingUsage{1: {Requests: 5}}), "below min_volume nothing is corrected")
}

func TestShareProtectedBindingsSurviveRebalance(t *testing.T) {
	require.True(t, IsShareRebalanceableBinding("anchor:abc"))
	require.True(t, IsShareRebalanceableBinding("openai:session"))
	require.False(t, IsShareRebalanceableBinding(openAIWSResponseAccountCacheKey("resp_1")))
	require.False(t, IsShareRebalanceableBinding(openAIHTTPResponseOwnerUserPrefix+"x"))
	require.False(t, IsShareRebalanceableBinding("openai:grok-video:abc"))
}
