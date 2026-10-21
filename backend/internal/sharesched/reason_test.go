package sharesched

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestActiveMigrationReasons: only realigning and returning are moves of
// share scheduling's own choosing. The rest are forced by the bound account,
// and retired codes left in counters from before an upgrade are not active.
func TestActiveMigrationReasons(t *testing.T) {
	active := map[Reason]bool{ReasonRealignCold: true, ReasonReturnCold: true}
	for _, r := range []Reason{
		ReasonOwnerContinuation, ReasonAffinityHit, ReasonAffinityWait, ReasonSpillRebind, ReasonSoftSpill,
		ReasonNewSession, ReasonSessionless, ReasonRealignCold, ReasonReturnCold, ReasonRebindUnavailable,
		ReasonTierOverflowSaturated, ReasonTierOverflowUnavailable, ReasonFailoverNext, ReasonModelRouting,
		ReasonModelRoutingFallback, ReasonResilFailureStreak, ReasonLastResortSoft,
		"return_immediate", "return_deadline", "drain_cold", "drain_deadline", "admin_rebalance", "",
	} {
		require.Equal(t, active[r], r.ActiveMigration(), "%q", r)
	}
}

// TestActiveMigrationsOnlyMoveColdSessions is why an active warm migration
// is a defect: the kernel takes active moves only once the cache is cold.
func TestActiveMigrationsOnlyMoveColdSessions(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	moved := 0
	for trial := 0; trial < 5000; trial++ {
		n := 2 + rng.IntN(6)
		cands := make([]Candidate, 0, n)
		for id := int64(1); id <= int64(n); id++ {
			c := member(id, 1+rng.IntN(3), 1+rng.IntN(10000))
			c.Eligible = rng.IntN(5) != 0
			c.StableSince = testNow.Add(-time.Duration(rng.IntN(7200)) * time.Second)
			cands = append(cands, c)
		}
		req := bound(fmt.Sprintf("a-%d", trial), int64(1+rng.IntN(n)), time.Duration(rng.IntN(600))*time.Second)
		d := Plan(req, cands)
		if !d.Reason.ActiveMigration() {
			continue
		}
		moved++
		require.False(t, req.Cache.Warm(req.Binding.Idle), "trial %d moved a warm session: %s", trial, d.Reason)
	}
	require.Positive(t, moved, "the trials include active moves")
}
