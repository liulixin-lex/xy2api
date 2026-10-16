package sharesched

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func sharePolicy(rules ...AccountRule) *Policy {
	p := DefaultPolicy()
	p.Scheduler = SchedulerShare
	p.Accounts = rules
	return &p
}

func rule(id int64, tier, weight int) AccountRule {
	return AccountRule{AccountID: id, Tier: tier, Weight: weight}
}

func eligible(ids ...int64) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, Candidate{AccountID: id, Platform: "anthropic", Class: "anthropic/oauth", Concurrency: 10, Eligible: true})
	}
	return out
}

func warmCache() CacheProfile {
	return CacheProfile{TTL: 5 * time.Minute, EstimatedTokens: 50_000, Cacheable: true}
}

func newSession(key string) Request {
	return Request{GroupID: 7, Key: key, Now: testNow, Cache: warmCache()}
}

func bound(key string, account int64, idle time.Duration) Request {
	req := newSession(key)
	req.Binding = Binding{AccountID: account, Idle: idle}
	return req
}

func firstStep(t *testing.T, d Decision) int64 {
	t.Helper()
	require.NotEmpty(t, d.Steps)
	return d.Steps[0].AccountID
}

func TestHRWHashIsFixed(t *testing.T) {
	// The order must be identical on every instance and across releases;
	// changing the hash would remap every session at once.
	require.InDelta(t, 0.6278323762764414, hrwUniform(7, "session-a", 244), 1e-15)
	require.Equal(t, hrwUniform(7, "session-a", 244), hrwUniform(7, "session-a", 244))
	require.NotEqual(t, hrwUniform(7, "session-a", 244), hrwUniform(8, "session-a", 244), "groups are salted independently")
}

func TestNewSessionShareIsProportionalToWeight(t *testing.T) {
	p := sharePolicy(rule(1, 1, 50), rule(2, 1, 30), rule(3, 1, 20))
	cands := eligible(1, 2, 3)
	counts := map[int64]int{}
	const sessions = 20000
	for i := 0; i < sessions; i++ {
		d := Plan(p, newSession(fmt.Sprintf("s-%d", i)), cands)
		require.Equal(t, ReasonNewSession, d.Reason)
		require.Equal(t, WriteIfAbsent, d.Write)
		counts[firstStep(t, d)]++
	}
	for id, want := range map[int64]float64{1: 0.5, 2: 0.3, 3: 0.2} {
		got := float64(counts[id]) / sessions
		require.InDelta(t, want, got, 0.01, "account %d share", id)
	}
}

func TestAccountRemovalMovesOnlyItsSessions(t *testing.T) {
	p := sharePolicy(rule(1, 1, 50), rule(2, 1, 30), rule(3, 1, 20))
	all := eligible(1, 2, 3)
	without3 := eligible(1, 2)
	moved := map[int64]int{}
	for i := 0; i < 5000; i++ {
		key := fmt.Sprintf("s-%d", i)
		before := firstStep(t, Plan(p, newSession(key), all))
		after := firstStep(t, Plan(p, newSession(key), without3))
		if before != 3 {
			require.Equal(t, before, after, "sessions of remaining accounts never move")
		} else {
			moved[after]++
		}
	}
	// Account 3's sessions spread over the rest by weight (50:30).
	total := float64(moved[1] + moved[2])
	require.InDelta(t, 50.0/80, float64(moved[1])/total, 0.04)
}

func TestWeightChangeMovesTheMinimum(t *testing.T) {
	before := sharePolicy(rule(1, 1, 50), rule(2, 1, 50))
	after := sharePolicy(rule(1, 1, 60), rule(2, 1, 40))
	cands := eligible(1, 2)
	moved := 0
	const sessions = 20000
	for i := 0; i < sessions; i++ {
		key := fmt.Sprintf("s-%d", i)
		a := firstStep(t, Plan(before, newSession(key), cands))
		b := firstStep(t, Plan(after, newSession(key), cands))
		if a != b {
			require.Equal(t, int64(2), a, "only the account that lost weight gives up sessions")
			moved++
		}
	}
	// The theoretical minimum is the share change: 10%.
	require.InDelta(t, 0.10, float64(moved)/sessions, 0.01)
}

func TestPlanIsDeterministic(t *testing.T) {
	p := sharePolicy(rule(1, 1, 5), rule(2, 1, 3), rule(3, 2, 2))
	cands := eligible(1, 2, 3)
	for i := 0; i < 200; i++ {
		req := bound(fmt.Sprintf("k-%d", i), int64(1+i%3), time.Duration(i)*time.Second)
		require.Equal(t, Plan(p, req, cands), Plan(p, req, cands))
	}
}

func TestTiersAreStrictForNewSessions(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 2000; trial++ {
		var rules []AccountRule
		var cands []Candidate
		for id := int64(1); id <= int64(2+rng.IntN(8)); id++ {
			rules = append(rules, rule(id, 1+rng.IntN(3), 1+rng.IntN(100)))
			c := eligible(id)[0]
			c.Eligible = rng.IntN(4) != 0
			c.Excluded = rng.IntN(8) == 0
			cands = append(cands, c)
		}
		p := sharePolicy(rules...)
		d := Plan(p, newSession(fmt.Sprintf("t-%d", trial)), cands)
		best := math.MaxInt
		for i, c := range cands {
			if c.Eligible && !c.Excluded {
				best = min(best, rules[i].Tier)
			}
		}
		if best == math.MaxInt {
			require.Empty(t, d.Steps)
			continue
		}
		require.Equal(t, best, d.Steps[0].Tier, "never place below an available tier")
		for i := 1; i < len(d.Steps); i++ {
			require.LessOrEqual(t, d.Steps[i-1].Tier, d.Steps[i].Tier, "steps descend tier by tier")
		}
	}
}

func TestSessionlessRequestsFollowWeightWithoutBinding(t *testing.T) {
	p := sharePolicy(rule(1, 1, 3), rule(2, 1, 1))
	cands := eligible(1, 2)
	counts := map[int64]int{}
	for i := 0; i < 8000; i++ {
		req := newSession(fmt.Sprintf("random-%d", i))
		req.Sessionless = true
		d := Plan(p, req, cands)
		require.Equal(t, ReasonSessionless, d.Reason)
		require.Equal(t, WriteNone, d.Write)
		counts[firstStep(t, d)]++
	}
	require.InDelta(t, 0.75, float64(counts[1])/8000, 0.02)
}

func TestBindingLifecycle(t *testing.T) {
	const key = "conversation"
	twoAccounts := sharePolicy(rule(1, 1, 1), rule(2, 1, 1))
	// Find which account is the HRW target for the key so the table can bind
	// sessions to the non-target account deliberately.
	target := firstStep(t, Plan(twoAccounts, newSession(key), eligible(1, 2)))
	other := int64(3) - target

	t.Run("warm hit keeps the binding even off target", func(t *testing.T) {
		d := Plan(twoAccounts, bound(key, other, time.Minute), eligible(1, 2))
		require.Equal(t, ReasonAffinityHit, d.Reason)
		require.Equal(t, other, firstStep(t, d))
		require.Equal(t, WriteRefresh, d.Write)
		require.Equal(t, WriteReplace, d.Spill)
		require.Equal(t, 5*time.Second, d.Wait, "50k tokens at 100ms/1k")
		require.Equal(t, 3, d.MaxWaiting)
	})
	t.Run("cold off-target realigns", func(t *testing.T) {
		d := Plan(twoAccounts, bound(key, other, 6*time.Minute), eligible(1, 2))
		require.Equal(t, ReasonRealignCold, d.Reason)
		require.Equal(t, target, firstStep(t, d))
		require.Equal(t, WriteReplace, d.Write)
	})
	t.Run("cold on target stays", func(t *testing.T) {
		d := Plan(twoAccounts, bound(key, target, time.Hour), eligible(1, 2))
		require.Equal(t, ReasonAffinityHit, d.Reason)
		require.Zero(t, d.Wait, "no cache left, no waiting")
	})
	t.Run("uncacheable prompt is always cold", func(t *testing.T) {
		req := bound(key, other, time.Second)
		req.Cache.Cacheable = false
		d := Plan(twoAccounts, req, eligible(1, 2))
		require.Equal(t, ReasonRealignCold, d.Reason)
	})
	t.Run("realign off keeps cold sessions", func(t *testing.T) {
		p := sharePolicy(rule(1, 1, 1), rule(2, 1, 1))
		p.Cache.Realign = RealignOff
		d := Plan(p, bound(key, other, time.Hour), eligible(1, 2))
		require.Equal(t, ReasonAffinityHit, d.Reason)
	})
	t.Run("short outage soft spills", func(t *testing.T) {
		cands := eligible(1, 2)
		cands[other-1].Eligible = false
		cands[other-1].Reason = ReasonRateLimited
		cands[other-1].RecoverAt = testNow.Add(time.Minute)
		d := Plan(twoAccounts, bound(key, other, 30*time.Second), cands)
		require.Equal(t, ReasonSoftSpill, d.Reason)
		require.Equal(t, target, firstStep(t, d))
		require.Equal(t, WriteNone, d.Write, "binding survives so the warm cache is reused on recovery")
	})
	t.Run("outage outliving the cache rebinds", func(t *testing.T) {
		cands := eligible(1, 2)
		cands[other-1].Eligible = false
		cands[other-1].RecoverAt = testNow.Add(4 * time.Minute)
		d := Plan(twoAccounts, bound(key, other, 30*time.Second), cands)
		require.Equal(t, ReasonRebindUnavailable, d.Reason, "4m30s exceeds 0.8 x 5m")
		require.Equal(t, WriteReplace, d.Write)
	})
	t.Run("long outage rebinds", func(t *testing.T) {
		p := sharePolicy(rule(1, 1, 1), rule(2, 1, 1))
		p.Affinity.SoftSpillMaxSeconds = 60
		cands := eligible(1, 2)
		cands[other-1].Eligible = false
		cands[other-1].RecoverAt = testNow.Add(2 * time.Minute)
		req := bound(key, other, 0)
		req.Cache.TTL = time.Hour
		require.Equal(t, ReasonRebindUnavailable, Plan(p, req, cands).Reason)
	})
	t.Run("unknown recovery rebinds", func(t *testing.T) {
		cands := eligible(1, 2)
		cands[other-1].Eligible = false
		require.Equal(t, ReasonRebindUnavailable, Plan(twoAccounts, bound(key, other, 0), cands).Reason)
	})
	t.Run("bound account gone rebinds", func(t *testing.T) {
		d := Plan(twoAccounts, bound(key, 99, 0), eligible(1, 2))
		require.Equal(t, ReasonRebindUnavailable, d.Reason)
		require.Equal(t, target, firstStep(t, d))
	})
	t.Run("failover moves to the next account", func(t *testing.T) {
		cands := eligible(1, 2)
		cands[other-1].Excluded = true
		d := Plan(twoAccounts, bound(key, other, 0), cands)
		require.Equal(t, ReasonFailoverNext, d.Reason)
		require.Equal(t, []Step{{AccountID: target, Tier: 1}}, d.Steps)
		require.Equal(t, WriteReplace, d.Write)
	})
	t.Run("failure streak moves the session with its own reason", func(t *testing.T) {
		// Gateway resilience: the session's avoid set excludes its account.
		cands := eligible(1, 2)
		cands[other-1].Excluded = true
		cands[other-1].Reason = ReasonResilFailureStreak
		d := Plan(twoAccounts, bound(key, other, 0), cands)
		require.Equal(t, ReasonResilFailureStreak, d.Reason)
		require.Equal(t, []Step{{AccountID: target, Tier: 1}}, d.Steps)
		require.Equal(t, WriteReplace, d.Write)
	})
}

func TestDrain(t *testing.T) {
	const key = "drain-session"
	until := testNow.Add(-time.Hour)
	p := sharePolicy(AccountRule{AccountID: 1, Tier: 1, Weight: 1, Drain: true}, rule(2, 1, 1))

	d := Plan(p, newSession(key), eligible(1, 2))
	require.Equal(t, int64(2), firstStep(t, d), "drained accounts take no new sessions")
	require.Equal(t, []Step{{AccountID: 2, Tier: 1}}, d.Steps)

	require.Equal(t, ReasonAffinityHit, Plan(p, bound(key, 1, time.Minute), eligible(1, 2)).Reason, "warm sessions stay")
	d = Plan(p, bound(key, 1, 10*time.Minute), eligible(1, 2))
	require.Equal(t, ReasonDrainCold, d.Reason)
	require.Equal(t, int64(2), firstStep(t, d))

	p.Accounts[0].DrainUntil = &until
	d = Plan(p, bound(key, 1, time.Second), eligible(1, 2))
	require.Equal(t, ReasonDrainDeadline, d.Reason, "deadline plus stagger has passed")

	future := testNow.Add(time.Hour)
	p.Accounts[0].DrainUntil = &future
	require.Equal(t, ReasonAffinityHit, Plan(p, bound(key, 1, time.Second), eligible(1, 2)).Reason)

	// When every open account is unusable, a drained one still serves.
	cands := eligible(1, 2)
	cands[1].Eligible = false
	d = Plan(p, newSession(key), cands)
	require.Equal(t, int64(1), firstStep(t, d))
}

func TestTierReturn(t *testing.T) {
	const key = "return-session"
	cands := eligible(1, 2)
	makePolicy := func(mode string, deadline int) *Policy {
		p := sharePolicy(rule(1, 1, 1), rule(2, 2, 1))
		p.Tiers.Return = mode
		p.Tiers.ReturnDeadlineSeconds = deadline
		return p
	}
	recent := func(c []Candidate, since time.Time) []Candidate {
		out := append([]Candidate(nil), c...)
		out[0].StableSince = since
		return out
	}

	require.Equal(t, ReasonAffinityHit, Plan(makePolicy(ReturnCold, 0), bound(key, 2, time.Minute), cands).Reason, "warm sessions stay low")
	d := Plan(makePolicy(ReturnCold, 0), bound(key, 2, 10*time.Minute), cands)
	require.Equal(t, ReasonReturnCold, d.Reason)
	require.Equal(t, int64(1), firstStep(t, d))
	require.Equal(t, ReasonAffinityHit, Plan(makePolicy(ReturnCold, 0), bound(key, 2, 10*time.Minute), recent(cands, testNow.Add(-time.Minute))).Reason,
		"tier 1 must be stable for stable_seconds before sessions return")

	require.Equal(t, ReasonReturnImmediate, Plan(makePolicy(ReturnImmediate, 0), bound(key, 2, time.Second), cands).Reason)
	require.Equal(t, ReasonAffinityHit, Plan(makePolicy(ReturnImmediate, 0), bound(key, 2, time.Second), recent(cands, testNow.Add(-time.Second))).Reason)

	require.Equal(t, ReasonAffinityHit, Plan(makePolicy(ReturnSessionEnd, 0), bound(key, 2, time.Hour), cands).Reason)

	p := makePolicy(ReturnCold, 600)
	require.Equal(t, ReasonAffinityHit, Plan(p, bound(key, 2, time.Second), recent(cands, testNow.Add(-5*time.Minute))).Reason)
	require.Equal(t, ReasonReturnDeadline, Plan(p, bound(key, 2, time.Second), recent(cands, testNow.Add(-21*time.Minute))).Reason,
		"deadline (10m) plus the maximum stagger (10m) has passed")
	require.Equal(t, ReasonAffinityHit, Plan(p, bound(key, 2, time.Second), cands).Reason,
		"no known recovery of tier 1 (it was only saturated): a warm session is not forced back")
	require.Equal(t, ReasonReturnCold, Plan(p, bound(key, 2, 10*time.Minute), cands).Reason, "it returns when its cache is cold")
}

func TestSpillOrderPrefersCacheDomainThenClass(t *testing.T) {
	p := sharePolicy(
		AccountRule{AccountID: 1, Tier: 1, Weight: 1, CacheDomain: "ws-a"},
		AccountRule{AccountID: 2, Tier: 1, Weight: 1, CacheDomain: "ws-a"},
		rule(3, 1, 1000),
		rule(4, 1, 1000),
		rule(5, 2, 1000),
	)
	cands := eligible(1, 2, 3, 4, 5)
	cands[3].Class = "anthropic/apikey"
	d := Plan(p, bound("spill", 1, time.Minute), cands)
	require.Equal(t, ReasonAffinityHit, d.Reason)
	require.Equal(t, []Step{{1, 1}, {2, 1}, {3, 1}, {4, 1}, {5, 2}}, d.Steps,
		"bound, same domain, same class, other class, then the next tier")

	reason, write := d.Outcome(1)
	require.Equal(t, ReasonSpillRebind, reason)
	require.Equal(t, WriteReplace, write)

	p.Affinity.OnSpill = OnSpillKeep
	reason, write = Plan(p, bound("spill", 1, time.Minute), cands).Outcome(2)
	require.Equal(t, ReasonSpillKeep, reason)
	require.Equal(t, WriteNone, write)

	// Accounts without a configured domain each form their own: account 3
	// shares nothing with 4, so its class peer 2 comes first.
	cands = eligible(1, 2, 3, 4, 5)
	cands[1].Class, cands[2].Class = "openai/oauth", "openai/oauth"
	d = Plan(p, bound("spill", 3, time.Minute), cands)
	require.Equal(t, []int64{3, 2}, []int64{d.Steps[0].AccountID, d.Steps[1].AccountID})
	explained := d.ExplainedEntries()
	require.Equal(t, "account:3", explained[2].CacheDomain)
	require.Equal(t, "ws-a", explained[0].CacheDomain)
	require.Empty(t, d.Entries[2].CacheDomain, "Plan itself leaves the implicit domain unnamed")
}

func TestSaturationAndFailurePolicies(t *testing.T) {
	cands := eligible(1, 2, 3)
	p := sharePolicy(rule(1, 1, 1), rule(2, 1, 1), rule(3, 2, 1))
	d := Plan(p, newSession("s"), cands)
	require.Len(t, d.Steps, 3)
	reason, write := d.Outcome(2)
	require.Equal(t, ReasonTierOverflowSaturated, reason)
	require.Equal(t, WriteIfAbsent, write)

	p.Tiers.OnSaturated = OnSaturatedWait
	d = Plan(p, newSession("s"), cands)
	require.Len(t, d.Steps, 2, "wait mode never spills below the active tier")

	p = sharePolicy(rule(1, 1, 1), rule(2, 1, 1), rule(3, 2, 1))
	failed := eligible(1, 2, 3)
	failed[0].Excluded, failed[1].Excluded = true, true
	d = Plan(p, newSession("s"), failed)
	require.Equal(t, []Step{{3, 2}}, d.Steps, "next_tier falls back after the tier failed")

	p.Tiers.OnFailed = OnFailedStop
	require.Empty(t, Plan(p, newSession("s"), failed).Steps, "stop refuses to fall after the tier failed")
	unavailable := eligible(1, 2, 3)
	unavailable[0].Eligible, unavailable[1].Eligible = false, false
	require.Equal(t, []Step{{3, 2}}, Plan(p, newSession("s"), unavailable).Steps, "stop only applies to failures in this request")
}

func TestTierOverflowUnavailableAndIdealAttribution(t *testing.T) {
	p := sharePolicy(rule(1, 1, 1), rule(2, 2, 1))
	cands := eligible(1, 2)
	cands[0].Eligible = false
	cands[0].Reason = ReasonQuotaExceeded
	d := Plan(p, newSession("s"), cands)
	require.Equal(t, ReasonTierOverflowUnavailable, d.Reason)
	require.Equal(t, int64(1), d.Ideal)
	require.Equal(t, ReasonQuotaExceeded, d.IdealSkipReason(2, nil))

	d = Plan(p, newSession("s"), eligible(1, 2))
	require.Equal(t, int64(1), d.Ideal)
	require.Empty(t, d.IdealSkipReason(1, nil))
	require.Equal(t, ReasonSaturated, d.IdealSkipReason(2, map[int64]Reason{1: ReasonSaturated}))

	// A configured account missing from the snapshot is still the ideal.
	d = Plan(p, newSession("s"), eligible(2))
	require.Equal(t, ReasonNotSchedulable, d.IdealSkipReason(2, nil))
}

func TestCorrectionScalesNewSessionsOnly(t *testing.T) {
	p := sharePolicy(rule(1, 1, 1), rule(2, 1, 1))
	cands := eligible(1, 2)
	counts := map[int64]int{}
	for i := 0; i < 10000; i++ {
		req := newSession(fmt.Sprintf("c-%d", i))
		req.Correction = map[int64]float64{1: 2, 2: 0.5}
		d := Plan(p, req, cands)
		counts[firstStep(t, d)]++
		require.Equal(t, firstStep(t, Plan(p, newSession(fmt.Sprintf("c-%d", i)), cands)), d.Ideal, "the ideal ignores correction")
	}
	require.InDelta(t, 0.8, float64(counts[1])/10000, 0.02, "weights 2:0.5")

	require.Equal(t, 1.0, CorrectionFactor(CorrectionOff, 0.5, 0.1))
	require.InDelta(t, math.Sqrt(2), CorrectionFactor(CorrectionGentle, 0.5, 0.25), 1e-9)
	require.Equal(t, 2.0, CorrectionFactor(CorrectionGentle, 0.5, 0.01))
	require.Equal(t, 0.25, CorrectionFactor(CorrectionStrong, 0.1, 0.9))
	require.Equal(t, 1.0, CorrectionFactor(CorrectionStrong, 0, 0.5))
}

func TestWaitBudget(t *testing.T) {
	w := DefaultPolicy().Affinity.Wait
	warm := warmCache()
	require.Equal(t, 5*time.Second, WaitBudget(w, warm, 0))
	big := warm
	big.EstimatedTokens = 400_000
	require.Equal(t, 15*time.Second, WaitBudget(w, big, 0), "capped at max_ms")
	require.Zero(t, WaitBudget(w, warm, 5*time.Minute), "cold cache is not worth waiting for")
	small := warm
	small.Cacheable = false
	require.Zero(t, WaitBudget(w, small, 0))
	w.Mode = WaitFixed
	require.Equal(t, 15*time.Second, WaitBudget(w, warm, 0))
	w.Mode = WaitOff
	require.Zero(t, WaitBudget(w, warm, 0))

	p := sharePolicy(rule(1, 1, 1))
	p.Affinity.Wait.MaxWaiting = 0
	require.Zero(t, Plan(p, bound("k", 1, 0), eligible(1)).Wait, "max_waiting 0 disables waiting")
}

func TestCacheProfile(t *testing.T) {
	require.Equal(t, time.Hour, CacheTTL(CacheFamilyAnthropic, true, 0))
	require.Equal(t, 5*time.Minute, CacheTTL(CacheFamilyAnthropic, false, 0))
	require.Equal(t, 30*time.Minute, CacheTTL(CacheFamilyOpenAI, false, 0))
	require.Equal(t, 5*time.Minute, CacheTTL(CacheFamilyGemini, false, 0))
	require.Equal(t, 2*time.Minute, CacheTTL(CacheFamilyOpenAI, false, 120))
	require.Equal(t, 2048, MinCacheableTokens(CacheFamilyAnthropic, "claude-3-5-haiku-20241022"))
	require.Equal(t, 4096, MinCacheableTokens(CacheFamilyAnthropic, "claude-opus-4-5"))
	require.Equal(t, 1024, MinCacheableTokens(CacheFamilyAnthropic, "claude-sonnet-4-5"))
	require.Equal(t, 1024, MinCacheableTokens(CacheFamilyOpenAI, "gpt-5.6"))
	profile := NewCacheProfile(CacheFamilyOpenAI, "gpt-5.6", false, 0, 1000)
	require.False(t, profile.Cacheable)
	require.True(t, NewCacheProfile(CacheFamilyOpenAI, "gpt-5.6", false, 0, 1024).Cacheable)
}

// TestPolicyChangesNeverMoveWarmSessions is the cache-protection property:
// weight changes, tier moves, correction, drain without a deadline and the
// cold return mode never move a session whose cache is still warm.
func TestPolicyChangesNeverMoveWarmSessions(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 5000; trial++ {
		n := 2 + rng.IntN(6)
		var rules []AccountRule
		cands := make([]Candidate, 0, n)
		correction := map[int64]float64{}
		for id := int64(1); id <= int64(n); id++ {
			rules = append(rules, AccountRule{AccountID: id, Tier: 1 + rng.IntN(3), Weight: 1 + rng.IntN(1000), Drain: rng.IntN(5) == 0,
				CacheDomain: []string{"", "", "d1", "d2"}[rng.IntN(4)]})
			c := eligible(id)[0]
			c.Eligible = rng.IntN(5) != 0
			c.StableSince = testNow.Add(-time.Duration(rng.IntN(7200)) * time.Second)
			cands = append(cands, c)
			correction[id] = 0.25 + rng.Float64()*3.75
		}
		p := sharePolicy(rules...)
		p.Tiers.Return = []string{ReturnCold, ReturnSessionEnd}[rng.IntN(2)]
		p.Tiers.OnSaturated = []string{OnSaturatedSpill, OnSaturatedWait}[rng.IntN(2)]
		p.Balance.Unit, p.Balance.Correction = UnitRequests, CorrectionStrong
		boundID := int64(1 + rng.IntN(n))
		req := bound(fmt.Sprintf("p-%d", trial), boundID, time.Duration(rng.IntN(299))*time.Second)
		req.Correction = correction
		d := Plan(p, req, cands)
		if !cands[boundID-1].Eligible {
			continue
		}
		require.Equal(t, ReasonAffinityHit, d.Reason, "trial %d", trial)
		require.Equal(t, boundID, firstStep(t, d), "trial %d", trial)
	}
}

func TestFrozenPolicyPlansIdentically(t *testing.T) {
	open := sharePolicy(rule(1, 1, 3), rule(2, 1, 1), rule(3, 2, 5), AccountRule{AccountID: 4, Tier: 1, Weight: 2, Drain: true})
	frozen := *open
	frozen.Freeze()
	cands := append(eligible(1, 2, 3, 4, 5), Candidate{AccountID: 6, Eligible: false, Reason: ReasonRateLimited})
	for i := range 200 {
		req := newSession(fmt.Sprintf("frozen-%d", i))
		if i%3 == 0 {
			req.Binding = Binding{AccountID: int64(i%6 + 1), Idle: time.Duration(i) * time.Second}
		}
		require.Equal(t, Plan(open, req, cands), Plan(&frozen, req, cands))
	}

	// Large policies are where the per-request index cost shows.
	rules := make([]AccountRule, 0, 100)
	ids := make([]int64, 0, 100)
	for id := int64(1); id <= 100; id++ {
		rules = append(rules, rule(id, 1, int(id%7)+1))
		ids = append(ids, id)
	}
	large := sharePolicy(rules...)
	largeFrozen := *large
	largeFrozen.Freeze()
	many := eligible(ids...)
	allocsOpen := testing.AllocsPerRun(50, func() { Plan(large, newSession("a"), many) })
	allocsFrozen := testing.AllocsPerRun(50, func() { Plan(&largeFrozen, newSession("a"), many) })
	require.Less(t, allocsFrozen, allocsOpen, "the frozen index is not rebuilt per request")
}
