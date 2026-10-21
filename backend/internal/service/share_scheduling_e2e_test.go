//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// The end-to-end tests drive the gateway selection like the handler does and
// answer each request from a synthetic upstream that keeps every account's
// prompt caches. A simulated clock drives binding idle times and upstream
// cache lifetimes, so cold windows need no sleeping; account health (rate
// limit resets) still follows the real clock.

// shareSimCache is the session binding store on the simulated clock: a
// binding expires and reports its remaining TTL from its last write.
type shareSimCache struct {
	*shareTestCache
	now     *time.Time
	touched map[string]time.Time
}

func (c *shareSimCache) touch(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touched[key] = *c.now
}

func (c *shareSimCache) remaining(key string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sessionBindings[key]; !ok {
		return 0, false
	}
	left := c.ttls[key] - c.now.Sub(c.touched[key])
	if left <= 0 {
		delete(c.sessionBindings, key)
		return 0, false
	}
	return left, true
}

func (c *shareSimCache) GetSessionAccountID(ctx context.Context, groupID int64, key string) (int64, error) {
	if _, ok := c.remaining(key); !ok {
		return 0, ErrStickySessionNotFound
	}
	return c.shareTestCache.GetSessionAccountID(ctx, groupID, key)
}

func (c *shareSimCache) GetSessionAccountIDWithTTL(ctx context.Context, groupID int64, key string) (int64, time.Duration, error) {
	left, ok := c.remaining(key)
	if !ok {
		return 0, 0, ErrStickySessionNotFound
	}
	id, err := c.shareTestCache.GetSessionAccountID(ctx, groupID, key)
	return id, left, err
}

func (c *shareSimCache) SetSessionAccountID(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) error {
	err := c.shareTestCache.SetSessionAccountID(ctx, groupID, key, accountID, ttl)
	c.touch(key)
	return err
}

func (c *shareSimCache) SetSessionAccountIDIfAbsent(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) (bool, error) {
	c.remaining(key) // drop an expired binding first, like Redis would
	ok, err := c.shareTestCache.SetSessionAccountIDIfAbsent(ctx, groupID, key, accountID, ttl)
	if ok {
		c.touch(key)
	}
	return ok, err
}

func (c *shareSimCache) RefreshSessionTTL(ctx context.Context, groupID int64, key string, ttl time.Duration) error {
	err := c.shareTestCache.RefreshSessionTTL(ctx, groupID, key, ttl)
	c.touch(key)
	return err
}

// shareSyntheticUpstream keeps each account's prompt caches: a session's
// prefix is read back while warm and every request rewrites it.
type shareSyntheticUpstream struct {
	ttl    time.Duration
	cached map[int64]map[string]shareSimPrefix
}

type shareSimPrefix struct {
	tokens int
	at     time.Time
}

func (u *shareSyntheticUpstream) serve(account int64, session string, prompt int, now time.Time) (read, write int) {
	if u.cached[account] == nil {
		u.cached[account] = map[string]shareSimPrefix{}
	}
	if p, ok := u.cached[account][session]; ok && now.Sub(p.at) <= u.ttl {
		read = p.tokens
	}
	u.cached[account][session] = shareSimPrefix{tokens: prompt, at: now}
	return read, prompt - read
}

type shareE2E struct {
	t        *testing.T
	env      *shareTestEnv
	cache    *shareSimCache
	upstream *shareSyntheticUpstream
	now      time.Time
	turns    map[string]int
	seq      int
	version  int64
}

func newShareE2E(t *testing.T, setup shareTestSetup, accounts ...Account) *shareE2E {
	t.Helper()
	env := newShareTestEnv(t, setup, accounts...)
	env.share.store = &shareMemoryStore{}
	e := &shareE2E{t: t, env: env, now: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), turns: map[string]int{}, version: 3,
		upstream: &shareSyntheticUpstream{ttl: 5 * time.Minute, cached: map[int64]map[string]shareSimPrefix{}}}
	e.cache = &shareSimCache{shareTestCache: env.cache, now: &e.now, touched: map[string]time.Time{}}
	env.gateway.cache = e.cache
	return e
}

// turn sends the session's next request, settles its usage and returns the
// serving account and the cache tokens read upstream.
func (e *shareE2E) turn(session string) (int64, int) {
	e.t.Helper()
	e.seq++
	ctx := context.WithValue(cacheableContext(), ctxkey.RequestID, fmt.Sprintf("e2e-%d", e.seq))
	result := e.env.selectAccount(e.t, ctx, session, nil)
	require.True(e.t, result.Acquired, "session %s", session)
	prompt := 10_000 + 1_000*e.turns[session]
	e.turns[session]++
	read, write := e.upstream.serve(result.Account.ID, session, prompt, e.now)
	e.env.share.observeUsage(ctx, &UsageLog{AccountID: result.Account.ID, Model: "claude-sonnet-4-5",
		CacheReadTokens: read, CacheCreationTokens: write})
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
	e.settle()
	return result.Account.ID, read
}

// settle waits for the asynchronous attribution so no observation is dropped.
func (e *shareE2E) settle() {
	for len(e.env.share.attribution) > 0 {
		time.Sleep(10 * time.Microsecond)
	}
}

func (e *shareE2E) bound(session string) int64 {
	e.cache.mu.Lock()
	defer e.cache.mu.Unlock()
	return e.cache.sessionBindings[session]
}

// setLoadFactors changes account priorities and load factors, as an
// administrator does in the account settings.
func (e *shareE2E) setLoadFactors(rules ...shareRule) {
	setup := shareTestSetup{rules: rules}
	for i := range e.env.repo.accounts {
		setup.apply(&e.env.repo.accounts[i])
	}
	for _, acc := range e.env.repo.accountsByID {
		setup.apply(acc)
	}
}

// rateLimit marks an account limited until the given real time; nil clears it.
func (e *shareE2E) rateLimit(id int64, until *time.Time) {
	e.env.repo.accountsByID[id].RateLimitResetAt = until
}

func (e *shareE2E) counter(field string) int64 { return e.env.counter(field) }

func (e *shareE2E) cacheCounter(field string) int64 {
	return e.env.kindCounters(shareStatsKindCache)[field]
}

func shareE2ESessions(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return out
}

// TestShareSchedulingE2EWeightsOutagesAndPolicyChanges is task 4.1: three
// accounts at 5:3:2 and 3000 sessions through outages and policy changes.
func TestShareSchedulingE2EWeightsOutagesAndPolicyChanges(t *testing.T) {
	const a, b, c = int64(1), int64(2), int64(3)
	accounts := []Account{{ID: a, Concurrency: 10_000}, {ID: b, Concurrency: 10_000}, {ID: c, Concurrency: 10_000}}
	e := newShareE2E(t, shareTestPolicy(sharesched.SchedulerShare,
		shareRule{AccountID: a, Tier: 1, Weight: 5},
		shareRule{AccountID: b, Tier: 1, Weight: 3},
		shareRule{AccountID: c, Tier: 1, Weight: 2}), accounts...)
	sessions := shareE2ESessions("conv", 3000)

	// New sessions follow the weights within one percentage point.
	placed := map[int64]int{}
	for _, s := range sessions {
		id, _ := e.turn(s)
		placed[id]++
	}
	// One percentage point is below the sampling noise of 3000 sessions (one
	// standard deviation is 0.7-0.9 points); the kernel test checks 1 point
	// on 10,000 sessions. Here each share stays within three deviations.
	for id, want := range map[int64]float64{a: 0.5, b: 0.3, c: 0.2} {
		require.InDelta(t, want, float64(placed[id])/3000, 3*math.Sqrt(want*(1-want)/3000), "account %d share", id)
	}

	// Returning sessions stay and read their cache.
	e.now = e.now.Add(30 * time.Second)
	for _, s := range sessions {
		before := e.bound(s)
		id, read := e.turn(s)
		require.Equal(t, before, id)
		require.Positive(t, read, "session %s reads its cache", s)
	}
	require.Equal(t, int64(3000), e.cacheCounter(shareStatsClassPrefix+shareCacheHit))

	var onA []string
	for _, s := range sessions {
		if e.bound(s) == a {
			onA = append(onA, s)
		}
	}
	require.Len(t, onA, placed[a])

	// A 429 with a two-minute reset: A's sessions spill softly and keep
	// their binding, then return to A after the reset and read its cache.
	reset := time.Now().Add(120 * time.Second)
	e.rateLimit(a, &reset)
	e.now = e.now.Add(10 * time.Second)
	for _, s := range onA {
		id, _ := e.turn(s)
		require.NotEqual(t, a, id)
		require.Equal(t, a, e.bound(s), "a short outage keeps the binding of %s", s)
	}
	require.Equal(t, int64(len(onA)), e.counter(shareStatsReasonPrefix+string(sharesched.ReasonSoftSpill)))
	e.rateLimit(a, nil)
	e.now = e.now.Add(120 * time.Second)
	for _, s := range onA {
		id, read := e.turn(s)
		require.Equal(t, a, id, "session %s returns after the reset", s)
		require.Positive(t, read, "session %s still finds its cache on A", s)
	}

	// A ten-minute limit rebinds A's sessions over B and C at 3:2, and the
	// report attributes the misses to the outage. By then the prefixes the
	// soft spill wrote on B and C have expired, so every switch misses.
	switchMisses := e.cacheCounter(shareStatsClassPrefix + shareCacheMissSwitchDown)
	reset = time.Now().Add(600 * time.Second)
	e.rateLimit(a, &reset)
	e.now = e.now.Add(200 * time.Second)
	moved := map[int64]int{}
	for _, s := range onA {
		id, read := e.turn(s)
		require.Zero(t, read)
		require.Equal(t, id, e.bound(s), "a long outage rebinds %s", s)
		moved[id]++
	}
	require.Zero(t, moved[a])
	require.InDelta(t, 0.6, float64(moved[b])/float64(len(onA)), 0.05, "B takes three fifths of A's sessions")
	require.Equal(t, int64(len(onA)), e.counter(shareStatsReasonPrefix+string(sharesched.ReasonRebindUnavailable)))
	require.Equal(t, int64(len(onA)), e.cacheCounter(shareStatsClassPrefix+shareCacheMissSwitchDown)-switchMisses)
	require.Equal(t, int64(len(onA)), e.counter(shareStatsHotMigrationField), "only the outage moved warm sessions")
	e.rateLimit(a, nil)

	// Every session talks once more so all caches are warm again; A is back,
	// but the sessions moved off it stay where their cache now is.
	e.now = e.now.Add(30 * time.Second)
	for _, s := range sessions {
		before := e.bound(s)
		id, _ := e.turn(s)
		require.Equal(t, before, id, "session %s", s)
	}

	// New load factors move no warm session; B nearly stops taking new ones.
	e.setLoadFactors(shareRule{AccountID: a, Tier: 1, Weight: 2}, shareRule{AccountID: b, Tier: 1, Weight: 1}, shareRule{AccountID: c, Tier: 1, Weight: 7})
	e.now = e.now.Add(30 * time.Second)
	onB := 0
	for _, s := range sessions {
		before := e.bound(s)
		id, read := e.turn(s)
		require.Equal(t, before, id, "warm session %s stays", s)
		require.Positive(t, read)
		if id == b {
			onB++
		}
	}
	require.Positive(t, onB, "the account with a lower load factor keeps its warm sessions")
	fresh := map[int64]int{}
	for _, s := range shareE2ESessions("after-change", 700) {
		id, _ := e.turn(s)
		fresh[id]++
	}
	require.InDelta(t, 0.1, float64(fresh[b])/700, 0.04)
	require.InDelta(t, 0.2, float64(fresh[a])/700, 0.05)

	// Once caches are cold, sessions realign to the new load factors, still
	// without a single hot migration.
	e.now = e.now.Add(6 * time.Minute)
	stayedOnB := 0
	for _, s := range sessions {
		if id, _ := e.turn(s); id == b {
			stayedOnB++
		}
	}
	require.Less(t, stayedOnB, onB/2, "most cold sessions leave B")
	require.Positive(t, e.counter(shareStatsReasonPrefix+string(sharesched.ReasonRealignCold)))
	require.Equal(t, int64(len(onA)), e.counter(shareStatsHotMigrationField), "load factor changes moved no warm session")
}

// TestShareSchedulingE2ETierReturnsInColdWindow is the tier part of task
// 4.1: sessions that overflowed to tier 2 stay while warm and return to tier
// 1 in their cold window.
func TestShareSchedulingE2ETierReturnsInColdWindow(t *testing.T) {
	const primary, backup = int64(1), int64(4)
	e := newShareE2E(t, shareTestPolicy(sharesched.SchedulerShare),
		Account{ID: primary, Priority: 1, Concurrency: 1000}, Account{ID: backup, Priority: 2, Concurrency: 1000})
	sessions := shareE2ESessions("tier", 200)
	for _, s := range sessions {
		id, _ := e.turn(s)
		require.Equal(t, primary, id)
	}

	reset := time.Now().Add(10 * time.Minute)
	e.rateLimit(primary, &reset)
	e.now = e.now.Add(30 * time.Second)
	for _, s := range sessions {
		id, _ := e.turn(s)
		require.Equal(t, backup, id)
		require.Equal(t, backup, e.bound(s))
	}
	forced := e.counter(shareStatsHotMigrationField)
	require.Equal(t, int64(len(sessions)), forced, "the outage forces warm sessions off tier 1")

	e.rateLimit(primary, nil)
	e.now = e.now.Add(30 * time.Second)
	for _, s := range sessions {
		id, read := e.turn(s)
		require.Equal(t, backup, id, "warm session %s stays on tier 2", s)
		require.Positive(t, read)
	}

	// Tier 1 has been healthy for the stable period (120 s of real time,
	// backdated here) and the caches have gone cold.
	e.env.share.ineligible.Store(primary, time.Now().Add(-sharesched.StableFor-time.Second).UnixNano())
	e.now = e.now.Add(6 * time.Minute)
	for _, s := range sessions {
		id, _ := e.turn(s)
		require.Equal(t, primary, id, "cold session %s returns to tier 1", s)
	}
	require.Equal(t, int64(len(sessions)), e.counter(shareStatsReasonPrefix+string(sharesched.ReasonReturnCold)))
	require.Equal(t, forced, e.counter(shareStatsHotMigrationField), "the return moved no warm session")
}

// TestShareSchedulingE2EFailoverOrderIsDeterministic is task 4.2: repeated
// failures of one session walk the same accounts on every instance, with
// accounts of the same class first and the next priority last.
func TestShareSchedulingE2EFailoverOrderIsDeterministic(t *testing.T) {
	rules := []shareRule{
		{AccountID: 1, Tier: 1, Weight: 1}, {AccountID: 2, Tier: 1, Weight: 1},
		{AccountID: 3, Tier: 1, Weight: 1}, {AccountID: 4, Tier: 1, Weight: 1},
		{AccountID: 5, Tier: 1, Weight: 1}, {AccountID: 6, Tier: 2, Weight: 1},
	}
	accounts := []Account{{ID: 1}, {ID: 2}, {ID: 3, Type: AccountTypeOAuth}, {ID: 4}, {ID: 5}, {ID: 6}}
	walk := func(env *shareTestEnv, session string) []int64 {
		env.cache.bind(session, 1, time.Hour-30*time.Second)
		excluded := map[int64]struct{}{1: {}}
		order := []int64{}
		for len(excluded) < len(accounts) {
			result := env.selectAccount(t, cacheableContext(), session, maps.Clone(excluded))
			order = append(order, result.Account.ID)
			excluded[result.Account.ID] = struct{}{}
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		}
		return order
	}
	instances := []*shareTestEnv{
		newShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShare, rules...), accounts...),
		newShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShare, rules...), accounts...),
	}
	for _, session := range shareE2ESessions("failover", 20) {
		want := walk(instances[0], session)
		require.Equal(t, int64(3), want[len(want)-2], "another account class comes after the same class")
		require.Equal(t, int64(6), want[len(want)-1], "the next priority comes last")
		for i := range 3 {
			require.Equal(t, want, walk(instances[i%2], session), "session %s, repetition %d", session, i)
		}
	}
}

// TestShareSchedulingE2ESaturatedLongSessionWaitsThenRebinds is task 4.3: a
// long warm session waits for its saturated account within the budget, then
// rebinds; the wait polls Redis on a bounded backoff.
func TestShareSchedulingE2ESaturatedLongSessionWaitsThenRebinds(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	target, other := env.targetFor(t, "long")
	env.cache.bind("long", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	// About 3000 prompt tokens at 100 ms per 1k tokens: a 300 ms budget.
	ctx := withShareRequestProfile(context.Background(), make([]byte, 12_000))
	start := time.Now()
	result := env.selectAccount(t, ctx, "long", nil)
	elapsed := time.Since(start)
	require.Equal(t, other, result.Account.ID)
	require.True(t, result.Acquired)
	require.Equal(t, other, env.cache.sessionBindings["long"], "after the wait the session rebinds")
	require.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "the budget is used before spilling")
	require.Equal(t, 1, env.conc.maxSeen[target], "the wait holds one queue slot")
	require.Zero(t, env.conc.waiting[target])

	// One slot try before the wait, then polls every 20 ms growing to
	// 250 ms: at most 1 + 6 polls within a 300 ms budget.
	env.conc.mu.Lock()
	tries := env.conc.attempts[target]
	env.conc.mu.Unlock()
	require.LessOrEqual(t, tries, 7, "slot tries on the saturated account")
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSpillRebind)))
	require.Zero(t, env.counter(shareStatsWaitHitField))
}

// shareFlakyRepo fails policy reads while down.
type shareFlakyRepo struct {
	*shareMemoryRepo
	down atomic.Bool
}

var errShareStoreDown = errors.New("policy store unavailable")

func (r *shareFlakyRepo) ListPolicies(ctx context.Context) ([]ShareSchedulingPolicyRow, error) {
	if r.down.Load() {
		return nil, errShareStoreDown
	}
	return r.shareMemoryRepo.ListPolicies(ctx)
}

func (r *shareFlakyRepo) GetPolicy(ctx context.Context, groupID int64) (*ShareSchedulingPolicyRow, error) {
	if r.down.Load() {
		return nil, errShareStoreDown
	}
	return r.shareMemoryRepo.GetPolicy(ctx, groupID)
}

// shareFlakySettings fails setting reads while down.
type shareFlakySettings struct {
	*shareMemorySettings
	down *atomic.Bool
}

func (s shareFlakySettings) GetValue(ctx context.Context, key string) (string, error) {
	if s.down.Load() {
		return "", errShareStoreDown
	}
	return s.shareMemorySettings.GetValue(ctx, key)
}

// shareDownCache fails binding reads, like an unreachable Redis.
type shareDownCache struct{ *shareTestCache }

func (shareDownCache) GetSessionAccountIDWithTTL(context.Context, int64, string) (int64, time.Duration, error) {
	return 0, 0, errShareStoreDown
}

// TestShareSchedulingE2ESwitchesFallBackWithoutMigrating is task 4.4: the
// global and group switches fall back at once without moving a binding, and
// an unavailable policy store never fails a request.
func TestShareSchedulingE2ESwitchesFallBackWithoutMigrating(t *testing.T) {
	accounts := []Account{{ID: 1}, {ID: 2}, {ID: 3}}
	env := newShareAdminEnv(t, accounts...)
	repo := &shareFlakyRepo{shareMemoryRepo: env.policies}
	env.share.repo = repo
	ctx := context.Background()
	_, err := env.share.UpdateSettings(ctx, true)
	require.NoError(t, err)
	_, err = env.save(t, 0, shareUpdate)
	require.NoError(t, err)

	sessions := shareE2ESessions("switch", 60)
	for _, s := range sessions {
		env.selectAccount(t, cacheableContext(), s, nil)
	}
	snapshot := func() map[string]int64 {
		env.cache.mu.Lock()
		defer env.cache.mu.Unlock()
		return maps.Clone(env.cache.sessionBindings)
	}
	bindings := snapshot()
	require.Len(t, bindings, len(sessions))
	served := func(label string) {
		t.Helper()
		for _, s := range sessions {
			result := env.selectAccount(t, cacheableContext(), s, nil)
			require.Equal(t, bindings[s], result.Account.ID, "%s: session %s keeps its account", label, s)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		}
		require.Equal(t, bindings, snapshot(), "%s: no binding moved", label)
	}

	// Global switch off: the original scheduler serves every session from
	// its existing binding.
	_, err = env.share.UpdateSettings(ctx, false)
	require.NoError(t, err)
	require.Nil(t, env.share.groupPolicy(shareTestGroupID))
	served("global switch off")

	// Group switch back to the original scheduler.
	_, err = env.share.UpdateSettings(ctx, true)
	require.NoError(t, err)
	_, err = env.save(t, 1, sub2apiUpdate)
	require.NoError(t, err)
	require.Nil(t, env.share.groupPolicy(shareTestGroupID))
	served("group switch off")

	// Back on share scheduling, the policy store goes down: the last known
	// policy keeps serving and nothing fails.
	_, err = env.save(t, 2, shareUpdate)
	require.NoError(t, err)
	repo.down.Store(true)
	require.Error(t, env.share.reloadAll(ctx))
	require.NotNil(t, env.share.groupPolicy(shareTestGroupID), "the last known policy stays in effect")
	served("policy store down")

	// A cold start without the store runs the original scheduler.
	settingsDown := &atomic.Bool{}
	settingsDown.Store(true)
	cold := NewShareSchedulingService(repo, &shareBusStore{shareAdminStore: env.store}, shareFlakySettings{shareMemorySettings: env.settings, down: settingsDown})
	cold.Start()
	t.Cleanup(cold.Stop)
	require.Nil(t, cold.groupPolicy(shareTestGroupID))
	env.gateway.shareScheduling = cold
	served("cold start without store")

	// An unreachable binding store places the request as new instead of
	// failing it.
	env.gateway.shareScheduling = env.share
	env.gateway.cache = shareDownCache{env.cache}
	result := env.selectAccount(t, cacheableContext(), sessions[0], nil)
	require.True(t, result.Acquired)
}
