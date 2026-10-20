package compat043

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// productionShapedV4 is a policy as the 0.3.0–0.4.3 editor stores it:
// shadow scheduler, equal default weight, rules with tiers and weights, and
// every other field written out with its default.
func productionShapedV4(t *testing.T) []byte {
	t.Helper()
	p := DefaultPolicy()
	p.Scheduler = SchedulerShadow
	p.Defaults.Weight = DefaultWeightEqual
	for i, r := range []struct{ tier, weight int }{{1, 33}, {1, 33}, {1, 33}, {2, 100}, {2, 1}, {1, 1}, {1, 1}, {3, 1}, {1, 1}, {15, 1}} {
		p.Accounts = append(p.Accounts, AccountRule{AccountID: int64(101 + i), Tier: r.tier, Weight: r.weight})
	}
	require.NoError(t, p.Validate())
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return raw
}

func TestCurrentVersionReadsStoredV4Policies(t *testing.T) {
	stored := productionShapedV4(t)
	p, err := sharesched.DecodePolicy(stored)
	require.NoError(t, err)
	require.Equal(t, sharesched.SchedulerShadow, p.Scheduler)
	require.Equal(t, sharesched.SchedulerSub2API, p.Effective(), "shadow runs the original scheduler")
	require.True(t, p.SessionKeep())

	// The 0.3.0 import of a controlled-scheduling policy (migration 270).
	imported := []byte(`{"schema":1,"scheduler":"sub2api","accounts":[{"account_id":5,"tier":2,"weight":1,"drain":true}]}`)
	p, err = sharesched.DecodePolicy(imported)
	require.NoError(t, err)
	require.Equal(t, sharesched.SchedulerSub2API, p.Effective())
}

// TestSavedPoliciesDecodeInV043 locks the rollback contract: whatever the
// current version writes decodes in 0.4.3, and a saved v4 policy still
// carries its original rules.
func TestSavedPoliciesDecodeInV043(t *testing.T) {
	stored := productionShapedV4(t)
	original, err := DecodePolicy(stored)
	require.NoError(t, err)

	for _, tc := range []struct {
		scheduler sharesched.Scheduler
		keep      bool
	}{{sharesched.SchedulerShare, true}, {sharesched.SchedulerShare, false}, {sharesched.SchedulerSub2API, true}} {
		patched, err := sharesched.PatchPolicyDocument(stored, tc.scheduler, tc.keep)
		require.NoError(t, err)
		old, err := DecodePolicy(patched)
		require.NoError(t, err, string(patched))
		require.Equal(t, Scheduler(tc.scheduler), old.Scheduler)
		require.Equal(t, original.Accounts, old.Accounts, "rollback restores the rules")
		require.Equal(t, original.Defaults, old.Defaults)
		require.Equal(t, original.Tiers, old.Tiers)
		require.Equal(t, original.Balance, old.Balance)
		require.Equal(t, original.Affinity.Wait, old.Affinity.Wait)
		if tc.keep {
			require.True(t, old.Affinity.Enabled)
			require.Equal(t, AffinityKeyAuto, old.Affinity.Key)
		} else {
			require.Equal(t, AffinityKeyNone, old.Affinity.Key)
		}

		again, err := sharesched.DecodePolicy(patched)
		require.NoError(t, err)
		require.Equal(t, tc.scheduler, again.Effective())
		require.Equal(t, tc.keep, again.SessionKeep())
	}

	for _, keep := range []bool{true, false} {
		for _, scheduler := range []sharesched.Scheduler{sharesched.SchedulerShare, sharesched.SchedulerSub2API} {
			fresh := sharesched.NewPolicyDocument(scheduler, keep)
			old, err := DecodePolicy(fresh)
			require.NoError(t, err, string(fresh))
			require.Equal(t, Scheduler(scheduler), old.Scheduler)
			require.Empty(t, old.Accounts)
			require.Equal(t, DefaultWeightConcurrency, old.Defaults.Weight, "a rollback weighs a new policy by concurrency")
		}
	}
}
