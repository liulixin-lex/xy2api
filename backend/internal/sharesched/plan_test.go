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

// member is an eligible candidate of a priority and weight (load factor).
func member(id int64, priority, weight int) Candidate {
	return Candidate{AccountID: id, Platform: "anthropic", Class: "anthropic/oauth", Priority: priority,
		LoadFactor: weight, Weight: float64(weight), Eligible: true}
}

func members(list ...Candidate) []Candidate { return list }

// equal returns eligible candidates of priority 1 and weight 1.
func equal(ids ...int64) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, member(id, 1, 1))
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
	cands := members(member(1, 1, 500), member(2, 1, 300), member(3, 1, 200))
	counts := map[int64]int{}
	const sessions = 20000
	for i := 0; i < sessions; i++ {
		d := Plan(newSession(fmt.Sprintf("s-%d", i)), cands)
		require.Equal(t, ReasonNewSession, d.Reason)
		require.Equal(t, WriteIfAbsent, d.Write)
		counts[firstStep(t, d)]++
	}
	for id, want := range map[int64]float64{1: 0.5, 2: 0.3, 3: 0.2} {
		got := float64(counts[id]) / sessions
		require.InDelta(t, want, got, 0.01, "account %d share", id)
	}
}

// TestExtremeWeightRatio: load factors span 1–10000; a 9999:100 ratio is
// placed in proportion without numeric trouble.
func TestExtremeWeightRatio(t *testing.T) {
	cands := members(member(1, 1, 9999), member(2, 1, 100))
	counts := map[int64]int{}
	const sessions = 50000
	for i := 0; i < sessions; i++ {
		counts[firstStep(t, Plan(newSession(fmt.Sprintf("x-%d", i)), cands))]++
	}
	require.InDelta(t, 9999.0/10099, float64(counts[1])/sessions, 0.002)
	require.Positive(t, counts[2])
}

// TestAdaptiveWeightsArePlacedInProportion: the adaptive factors make
// weights fractional; placement follows them, and explain keeps the load
// factor apart from the effective weight.
func TestAdaptiveWeightsArePlacedInProportion(t *testing.T) {
	cands := members(member(1, 1, 100), member(2, 1, 100), member(3, 1, 1))
	cands[1].Weight = 20
	cands[2].Weight = 0.05
	counts := map[int64]int{}
	const sessions = 40000
	var d Decision
	for i := 0; i < sessions; i++ {
		d = Plan(newSession(fmt.Sprintf("f-%d", i)), cands)
		counts[firstStep(t, d)]++
	}
	total := 100 + 20 + 0.05
	require.InDelta(t, 100/total, float64(counts[1])/sessions, 0.01)
	require.InDelta(t, 20/total, float64(counts[2])/sessions, 0.01)
	for _, e := range d.Entries {
		if e.AccountID == 2 {
			require.Equal(t, 100, e.LoadFactor)
			require.InDelta(t, 20, e.EffectiveWeight, 1e-9)
		}
	}
}

func TestNonPositiveWeightCountsAsOne(t *testing.T) {
	cands := members(member(1, 1, 0), member(2, 1, -5))
	counts := map[int64]int{}
	for i := 0; i < 4000; i++ {
		counts[firstStep(t, Plan(newSession(fmt.Sprintf("z-%d", i)), cands))]++
	}
	require.InDelta(t, 0.5, float64(counts[1])/4000, 0.03)
}

func TestAccountRemovalMovesOnlyItsSessions(t *testing.T) {
	all := members(member(1, 1, 50), member(2, 1, 30), member(3, 1, 20))
	without3 := all[:2]
	moved := map[int64]int{}
	for i := 0; i < 5000; i++ {
		key := fmt.Sprintf("s-%d", i)
		before := firstStep(t, Plan(newSession(key), all))
		after := firstStep(t, Plan(newSession(key), without3))
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
	before := members(member(1, 1, 50), member(2, 1, 50))
	after := members(member(1, 1, 60), member(2, 1, 40))
	moved := 0
	const sessions = 20000
	for i := 0; i < sessions; i++ {
		key := fmt.Sprintf("s-%d", i)
		a := firstStep(t, Plan(newSession(key), before))
		b := firstStep(t, Plan(newSession(key), after))
		if a != b {
			require.Equal(t, int64(2), a, "only the account that lost weight gives up sessions")
			moved++
		}
	}
	// The theoretical minimum is the share change: 10%.
	require.InDelta(t, 0.10, float64(moved)/sessions, 0.01)
}

func TestPlanIsDeterministic(t *testing.T) {
	cands := members(member(1, 1, 5), member(2, 1, 3), member(3, 2, 2))
	for i := 0; i < 200; i++ {
		req := bound(fmt.Sprintf("k-%d", i), int64(1+i%3), time.Duration(i)*time.Second)
		require.Equal(t, Plan(req, cands), Plan(req, cands))
	}
}

// TestTiersAreStrictForNewSessions: the tier is the account priority.
func TestTiersAreStrictForNewSessions(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 2000; trial++ {
		var cands []Candidate
		for id := int64(1); id <= int64(2+rng.IntN(8)); id++ {
			c := member(id, 1+rng.IntN(3), 1+rng.IntN(100))
			c.Eligible = rng.IntN(4) != 0
			c.Excluded = rng.IntN(8) == 0
			cands = append(cands, c)
		}
		d := Plan(newSession(fmt.Sprintf("t-%d", trial)), cands)
		best := math.MaxInt
		for _, c := range cands {
			if c.Eligible && !c.Excluded {
				best = min(best, c.Priority)
			}
		}
		if best == math.MaxInt {
			require.Empty(t, d.Steps)
			continue
		}
		require.Equal(t, best, d.Steps[0].Priority, "never place below an available priority")
		require.Equal(t, best, d.ActivePriority)
		for i := 1; i < len(d.Steps); i++ {
			require.LessOrEqual(t, d.Steps[i-1].Priority, d.Steps[i].Priority, "steps descend priority by priority")
		}
	}
}

func TestSessionlessRequestsFollowWeightWithoutBinding(t *testing.T) {
	cands := members(member(1, 1, 3), member(2, 1, 1))
	counts := map[int64]int{}
	for i := 0; i < 8000; i++ {
		req := newSession(fmt.Sprintf("random-%d", i))
		req.Sessionless = true
		d := Plan(req, cands)
		require.Equal(t, ReasonSessionless, d.Reason)
		require.Equal(t, WriteNone, d.Write)
		counts[firstStep(t, d)]++
	}
	require.InDelta(t, 0.75, float64(counts[1])/8000, 0.02)
}

func TestBindingLifecycle(t *testing.T) {
	const key = "conversation"
	two := equal(1, 2)
	// Find which account is the HRW target for the key so the table can bind
	// sessions to the non-target account deliberately.
	target := firstStep(t, Plan(newSession(key), two))
	other := int64(3) - target

	t.Run("warm hit keeps the binding even off target", func(t *testing.T) {
		d := Plan(bound(key, other, time.Minute), equal(1, 2))
		require.Equal(t, ReasonAffinityHit, d.Reason)
		require.Equal(t, other, firstStep(t, d))
		require.Equal(t, WriteRefresh, d.Write)
		require.Equal(t, WriteReplace, d.Spill)
		require.Equal(t, 5*time.Second, d.Wait, "50k tokens at 100ms/1k")
		require.Equal(t, MaxWaiting, d.MaxWaiting)
	})
	t.Run("cold off-target realigns", func(t *testing.T) {
		d := Plan(bound(key, other, 6*time.Minute), equal(1, 2))
		require.Equal(t, ReasonRealignCold, d.Reason)
		require.Equal(t, target, firstStep(t, d))
		require.Equal(t, WriteReplace, d.Write)
	})
	t.Run("cold on target stays", func(t *testing.T) {
		d := Plan(bound(key, target, time.Hour), equal(1, 2))
		require.Equal(t, ReasonAffinityHit, d.Reason)
		require.Zero(t, d.Wait, "no cache left, no waiting")
	})
	t.Run("uncacheable prompt is always cold", func(t *testing.T) {
		req := bound(key, other, time.Second)
		req.Cache.Cacheable = false
		d := Plan(req, equal(1, 2))
		require.Equal(t, ReasonRealignCold, d.Reason)
	})
	t.Run("short outage soft spills", func(t *testing.T) {
		cands := equal(1, 2)
		cands[other-1].Eligible = false
		cands[other-1].Reason = ReasonRateLimited
		cands[other-1].RecoverAt = testNow.Add(time.Minute)
		d := Plan(bound(key, other, 30*time.Second), cands)
		require.Equal(t, ReasonSoftSpill, d.Reason)
		require.Equal(t, target, firstStep(t, d))
		require.Equal(t, WriteNone, d.Write, "binding survives so the warm cache is reused on recovery")
	})
	t.Run("outage outliving the cache rebinds", func(t *testing.T) {
		cands := equal(1, 2)
		cands[other-1].Eligible = false
		cands[other-1].RecoverAt = testNow.Add(4 * time.Minute)
		d := Plan(bound(key, other, 30*time.Second), cands)
		require.Equal(t, ReasonRebindUnavailable, d.Reason, "4m30s exceeds 0.8 x 5m")
		require.Equal(t, WriteReplace, d.Write)
	})
	t.Run("unknown recovery rebinds", func(t *testing.T) {
		// Adapters only give a recovery time for soft penalties.
		cands := equal(1, 2)
		cands[other-1].Eligible = false
		require.Equal(t, ReasonRebindUnavailable, Plan(bound(key, other, 0), cands).Reason)
	})
	t.Run("bound account gone rebinds", func(t *testing.T) {
		d := Plan(bound(key, 99, 0), equal(1, 2))
		require.Equal(t, ReasonRebindUnavailable, d.Reason)
		require.Equal(t, target, firstStep(t, d))
	})
	t.Run("failover moves to the next account", func(t *testing.T) {
		cands := equal(1, 2)
		cands[other-1].Excluded = true
		d := Plan(bound(key, other, 0), cands)
		require.Equal(t, ReasonFailoverNext, d.Reason)
		require.Equal(t, []Step{{AccountID: target, Priority: 1}}, d.Steps)
		require.Equal(t, WriteReplace, d.Write)
	})
	t.Run("failure streak moves the session with its own reason", func(t *testing.T) {
		// Gateway resilience: the session's avoid set excludes its account.
		cands := equal(1, 2)
		cands[other-1].Excluded = true
		cands[other-1].Reason = ReasonResilFailureStreak
		d := Plan(bound(key, other, 0), cands)
		require.Equal(t, ReasonResilFailureStreak, d.Reason)
		require.Equal(t, []Step{{AccountID: target, Priority: 1}}, d.Steps)
		require.Equal(t, WriteReplace, d.Write)
	})
}

func TestTierReturn(t *testing.T) {
	const key = "return-session"
	cands := members(member(1, 1, 1), member(2, 2, 1))
	recent := func(c []Candidate, since time.Time) []Candidate {
		out := append([]Candidate(nil), c...)
		out[0].StableSince = since
		return out
	}

	require.Equal(t, ReasonAffinityHit, Plan(bound(key, 2, time.Minute), cands).Reason, "warm sessions stay low")
	d := Plan(bound(key, 2, 10*time.Minute), cands)
	require.Equal(t, ReasonReturnCold, d.Reason)
	require.Equal(t, int64(1), firstStep(t, d))
	require.Equal(t, ReasonAffinityHit, Plan(bound(key, 2, 10*time.Minute), recent(cands, testNow.Add(-time.Minute))).Reason,
		"the higher priority must be stable for 120 s before sessions return")
	require.Equal(t, ReasonReturnCold, Plan(bound(key, 2, 10*time.Minute), recent(cands, testNow.Add(-3*time.Minute))).Reason)
}

func TestSpillOrderPrefersTheSameClass(t *testing.T) {
	cands := members(member(1, 1, 1), member(2, 1, 1000), member(3, 1, 1000), member(4, 2, 1000))
	cands[1].Class = "anthropic/apikey"
	d := Plan(bound("spill", 1, time.Minute), cands)
	require.Equal(t, ReasonAffinityHit, d.Reason)
	require.Equal(t, []Step{{1, 1}, {3, 1}, {2, 1}, {4, 2}}, d.Steps,
		"bound, same class, other class, then the next priority")

	reason, write := d.Outcome(1)
	require.Equal(t, ReasonSpillRebind, reason)
	require.Equal(t, WriteReplace, write)
	reason, write = d.Outcome(3)
	require.Equal(t, ReasonSpillRebind, reason)
	require.Equal(t, WriteReplace, write)
}

func TestSaturationAndFailureOverflow(t *testing.T) {
	cands := members(member(1, 1, 1), member(2, 1, 1), member(3, 2, 1))
	d := Plan(newSession("s"), cands)
	require.Len(t, d.Steps, 3, "saturated priorities spill to the next one")
	reason, write := d.Outcome(2)
	require.Equal(t, ReasonTierOverflowSaturated, reason)
	require.Equal(t, WriteIfAbsent, write)

	failed := members(member(1, 1, 1), member(2, 1, 1), member(3, 2, 1))
	failed[0].Excluded, failed[1].Excluded = true, true
	d = Plan(newSession("s"), failed)
	require.Equal(t, []Step{{3, 2}}, d.Steps, "a failed priority falls back to the next one")
}

func TestTierOverflowUnavailableAndIdealAttribution(t *testing.T) {
	cands := members(member(1, 1, 1), member(2, 2, 1))
	cands[0].Eligible = false
	cands[0].Reason = ReasonQuotaExceeded
	d := Plan(newSession("s"), cands)
	require.Equal(t, ReasonTierOverflowUnavailable, d.Reason)
	require.Equal(t, int64(1), d.Ideal, "the ideal ignores health")

	d = Plan(newSession("s"), members(member(1, 1, 1), member(2, 2, 1)))
	require.Equal(t, int64(1), d.Ideal)
}

func TestWaitBudget(t *testing.T) {
	warm := warmCache()
	require.Equal(t, 5*time.Second, WaitBudget(warm, 0))
	big := warm
	big.EstimatedTokens = 400_000
	require.Equal(t, MaxWait, WaitBudget(big, 0), "capped at 15 s")
	require.Zero(t, WaitBudget(warm, 5*time.Minute), "cold cache is not worth waiting for")
	small := warm
	small.Cacheable = false
	require.Zero(t, WaitBudget(small, 0))
}

func TestCacheProfile(t *testing.T) {
	require.Equal(t, time.Hour, CacheTTL(CacheFamilyAnthropic, true))
	require.Equal(t, 5*time.Minute, CacheTTL(CacheFamilyAnthropic, false))
	require.Equal(t, 30*time.Minute, CacheTTL(CacheFamilyOpenAI, false))
	require.Equal(t, 5*time.Minute, CacheTTL(CacheFamilyGemini, false))
	require.Equal(t, 2048, MinCacheableTokens(CacheFamilyAnthropic, "claude-3-5-haiku-20241022"))
	require.Equal(t, 4096, MinCacheableTokens(CacheFamilyAnthropic, "claude-opus-4-5"))
	require.Equal(t, 1024, MinCacheableTokens(CacheFamilyAnthropic, "claude-sonnet-4-5"))
	require.Equal(t, 1024, MinCacheableTokens(CacheFamilyOpenAI, "gpt-5.6"))
	profile := NewCacheProfile(CacheFamilyOpenAI, "gpt-5.6", false, 1000)
	require.False(t, profile.Cacheable)
	require.True(t, NewCacheProfile(CacheFamilyOpenAI, "gpt-5.6", false, 1024).Cacheable)
}

// TestWeightAndPriorityChangesNeverMoveWarmSessions is the cache-protection
// property: load factor and priority changes never move a session whose
// cache is still warm.
func TestWeightAndPriorityChangesNeverMoveWarmSessions(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 5000; trial++ {
		n := 2 + rng.IntN(6)
		cands := make([]Candidate, 0, n)
		for id := int64(1); id <= int64(n); id++ {
			c := member(id, 1+rng.IntN(3), 1+rng.IntN(10000))
			c.Eligible = rng.IntN(5) != 0
			c.StableSince = testNow.Add(-time.Duration(rng.IntN(7200)) * time.Second)
			cands = append(cands, c)
		}
		boundID := int64(1 + rng.IntN(n))
		req := bound(fmt.Sprintf("p-%d", trial), boundID, time.Duration(rng.IntN(299))*time.Second)
		d := Plan(req, cands)
		if !cands[boundID-1].Eligible {
			continue
		}
		require.Equal(t, ReasonAffinityHit, d.Reason, "trial %d", trial)
		require.Equal(t, boundID, firstStep(t, d), "trial %d", trial)
	}
}

func TestExplainEntries(t *testing.T) {
	cands := members(member(1, 1, 30), member(2, 2, 999))
	cands[1].Eligible, cands[1].Reason = false, ReasonRateLimited
	d := Plan(newSession("e"), cands)
	require.Len(t, d.Entries, 2)
	e := d.Entries[0]
	require.Equal(t, []any{int64(1), 1, 30, "anthropic/oauth", true, false, Reason(""), 1},
		[]any{e.AccountID, e.Priority, e.LoadFactor, e.Class, e.Eligible, e.Excluded, e.Reason, e.Rank})
	require.Equal(t, 999, d.Entries[1].LoadFactor)
	require.Equal(t, ReasonRateLimited, d.Entries[1].Reason)
}
