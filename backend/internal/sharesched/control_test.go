package sharesched

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDisabledAndStrictDrainContract(t *testing.T) {
	p := sharePolicy(rule(1, 1, 1), rule(2, 1, 1))
	p.Accounts[0].Drain = true
	cands := eligible(1, 2)
	cands[1].Eligible = false
	require.Empty(t, Plan(p, newSession("strict"), cands).Steps)
	require.Equal(t, int64(1), firstStep(t, Plan(p, bound("strict", 1, time.Second), cands)))
	require.Empty(t, Plan(p, bound("strict", 1, time.Hour), cands).Steps)
	p.Accounts[0].DrainFallback = true
	require.Equal(t, ReasonEmergencyDrain, Plan(p, newSession("strict"), cands).Reason)
	p.Accounts[0].Disabled = true
	require.Empty(t, Plan(p, newSession("strict"), cands).Steps)
	require.Empty(t, Plan(p, bound("strict", 1, time.Second), cands).Steps)
}
func TestAllDisabledIsAnExplicitValidStop(t *testing.T) {
	p := sharePolicy(rule(1, 1, 1), rule(2, 1, 1))
	for i := range p.Accounts {
		p.Accounts[i].Disabled = true
	}
	require.NoError(t, p.ValidateMembers(map[int64]Member{1: {}, 2: {}}))
	require.Empty(t, Plan(p, newSession("stop"), eligible(1, 2)).Steps)
	p.Accounts[0].Disabled = false
	p.Accounts[0].Drain = true
	require.Error(t, p.ValidateMembers(map[int64]Member{1: {}, 2: {}}))
	p.Accounts[0].DrainFallback = true
	require.NoError(t, p.ValidateMembers(map[int64]Member{1: {}, 2: {}}))
}

func TestDrainDeadlineOverridesEmergencyFallback(t *testing.T) {
	p := sharePolicy(rule(1, 1, 1))
	p.Accounts[0].Drain = true
	p.Accounts[0].DrainFallback = true
	past := testNow.Add(-time.Second)
	p.Accounts[0].DrainUntil = &past
	require.Empty(t, Plan(p, newSession("deadline"), eligible(1)).Steps)
	future := testNow.Add(time.Hour)
	p.Accounts[0].DrainUntil = &future
	req := newSession("stateless")
	req.Sessionless = true
	d := Plan(p, req, eligible(1))
	require.Equal(t, ReasonEmergencyDrain, d.Reason)
	require.Equal(t, WriteNone, d.Write)
}
