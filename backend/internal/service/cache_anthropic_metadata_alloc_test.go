//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// L-03 closed states (M4 kickoff §7.1): the L-03 control arm (l04_only) and an
// empty metadata_account_ids cost what L-03 closed by capability costs, with
// L-04 applied identically in every arm.
func TestL03ControlArmAndEmptyAccountsAllocsEqualClosed(t *testing.T) {
	for _, tc := range []struct {
		name, arm, skip string
		setup           func(*CachePolicySnapshot, int)
	}{
		// The exact boundary h == metadata_rollout_percent is the control arm.
		{"control_arm", "off", "arm_off", func(q *CachePolicySnapshot, bucket int) { q.anchorPolicy.metadataRollout = bucket }},
		{"empty_accounts", "on", "not_in_metadata_accounts", func(q *CachePolicySnapshot, _ int) { q.anchorPolicy.metadataAccounts = nil }},
		{"applied", "on", "none", func(*CachePolicySnapshot, int) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, trace := range []bool{true, false} {
				s, c, k, p, a := l03RequestFixture(t, trace)
				state := *s.providerTraceEngine.cachePolicies.Snapshot()
				s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
				bucket, ok := p.AnchorV2.cohortForGroup(31)
				require.True(t, ok)
				require.Positive(t, bucket)
				s.ReleaseCacheAnchor(p)
				tc.setup(&state, bucket)
				closed := state
				closed.anchorPolicy.metadataAvailable = false
				body := p.Body.Bytes()
				op := func() {
					s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
					s.GenerateSessionHash(p)
					ctx := s.beginCacheMetadataAttempt(c.Request.Context(), c, p, a)
					_ = s.applyCacheMetadata(ctx, c, a, body, false)
					s.ReleaseCacheAnchor(p)
				}
				c.Set(cachePolicySnapshotGinKey, &state)
				if trace {
					op()
					tr := cacheTraceFromGin(c).data
					require.Equal(t, "none", tr.L04Skip, "L-04 applies in every arm")
					require.Equal(t, tc.arm, tr.L03Arm)
					require.Equal(t, tc.skip, tr.L03Skip)
					continue
				}
				got := cacheArmAllocs([]func(){
					func() { c.Set(cachePolicySnapshotGinKey, &closed) },
					func() { c.Set(cachePolicySnapshotGinKey, &state) },
				}, op)
				t.Logf("allocs/op l03_closed=%v %s=%v", got[0], tc.name, got[1])
				if tc.skip == "none" {
					require.Greater(t, got[1], got[0], "the gate must see applied metadata")
					continue
				}
				require.Equal(t, got[0], got[1], "a closed L-03 state must cost what L-03 closed by capability costs")
				require.Empty(t, s.anchorInflight.entries)
			}
		})
	}
}
