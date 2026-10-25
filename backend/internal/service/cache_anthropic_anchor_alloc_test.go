//go:build unit

package service

import (
	"math"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// Closed-state allocation gate (M4 kickoff §5, §7.1): a request skipped before
// its anchor costs exactly what the closed build costs. Arms alternate in one
// fixture and each keeps its minimum over cacheAllocRounds AllocsPerRun rounds,
// after one uncounted round so lazily built shared state is warm in every arm.
const cacheAllocRounds, cacheAllocRuns = 5, 50

func cacheArmAllocs(arms []func(), op func()) []float64 {
	best := make([]float64, len(arms))
	for i, arm := range arms {
		best[i] = math.Inf(1)
		arm()
		testing.AllocsPerRun(cacheAllocRuns, op)
	}
	for round := 0; round < cacheAllocRounds; round++ {
		for i, arm := range arms {
			arm()
			best[i] = math.Min(best[i], testing.AllocsPerRun(cacheAllocRuns, op))
		}
	}
	return best
}

func TestL04OutOfScopeAllocsEqualClosedBuild(t *testing.T) {
	listed := func(ids ...int64) func(*GatewayService, *APIKey, *CachePolicySnapshot) {
		return func(_ *GatewayService, _ *APIKey, q *CachePolicySnapshot) { q.anchorPolicy.groups = ids }
	}
	for _, tc := range []struct {
		name, skip, platform string
		compat               bool
		setup                func(*GatewayService, *APIKey, *CachePolicySnapshot)
	}{
		{name: "group_not_in_list", skip: "not_in_group", setup: listed(55)},
		// gemini_anchor=true keeps the policy enabled with no Anthropic group.
		{name: "empty_group_list", skip: "not_in_group", setup: listed()},
		{name: "claude_code_fallback_not_in_list", skip: "not_in_group", setup: func(s *GatewayService, k *APIKey, q *CachePolicySnapshot) {
			fallback := int64(77)
			k.Group.ClaudeCodeOnly = true
			k.Group.FallbackGroupID = &fallback
			s.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{31: k.Group, 77: {ID: 77, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}}}
			q.anchorPolicy.groups = []int64{55}
		}},
		{name: "compat_not_in_list", skip: "not_in_group", compat: true, setup: listed(55)},
		{name: "compat_listed", skip: "empty_anchor", compat: true},
		{name: "other_platform", skip: "not_anthropic_group", platform: PlatformAntigravity, setup: func(_ *GatewayService, k *APIKey, _ *CachePolicySnapshot) {
			k.Group.Platform = PlatformAntigravity
		}},
		{name: "share_active", skip: "share_active", setup: func(s *GatewayService, _ *APIKey, _ *CachePolicySnapshot) {
			share := NewShareSchedulingService(nil, nil, nil)
			share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true}, groups: map[int64]*shareGroupPolicy{31: {groupID: 31, policy: sharesched.Policy{Scheduler: sharesched.SchedulerShare}}}})
			s.shareScheduling = share
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := tc.platform
			if platform == "" {
				platform = PlatformAnthropic
			}
			// The traced fixture pins the intended reason; the untraced one is measured.
			for _, trace := range []bool{true, false} {
				s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, trace)
				state := *s.providerTraceEngine.cachePolicies.Snapshot()
				if tc.setup != nil {
					tc.setup(s, k, &state)
				}
				closed := state
				closed.master = false
				op := func() {
					if tc.compat {
						s.PrepareCompatCacheAnchor(c, k, p, platform)
					} else {
						s.PrepareCacheAnchor(c, k, p, platform, false)
					}
					s.GenerateSessionHash(p)
					_ = s.CacheAnchorApplied(p)
					s.ReleaseCacheAnchor(p)
				}
				c.Set(cachePolicySnapshotGinKey, &state)
				if trace {
					op()
					require.Equal(t, tc.skip, cacheTraceFromGin(c).data.L04Skip)
					require.Nil(t, p.cacheL04)
					continue
				}
				got := cacheArmAllocs([]func(){
					func() { c.Set(cachePolicySnapshotGinKey, &closed) },
					func() { c.Set(cachePolicySnapshotGinKey, &state) },
				}, op)
				t.Logf("allocs/op closed=%v %s=%v", got[0], tc.name, got[1])
				require.Equal(t, got[0], got[1], "skipped before the anchor must cost what the closed build costs")
				require.Nil(t, p.cacheL04)
				if repo, ok := s.groupRepo.(*mockGroupRepoForGateway); ok {
					require.Zero(t, repo.getByIDLiteCalls, "an unlisted fallback group needs no lookup")
				}
			}
		})
	}
	t.Run("gate_detects_applied_anchor", func(t *testing.T) {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, false)
		state := *s.providerTraceEngine.cachePolicies.Snapshot()
		closed := state
		closed.master = false
		got := cacheArmAllocs([]func(){
			func() { c.Set(cachePolicySnapshotGinKey, &closed) },
			func() { c.Set(cachePolicySnapshotGinKey, &state) },
		}, func() {
			s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
			s.GenerateSessionHash(p)
			s.ReleaseCacheAnchor(p)
		})
		t.Logf("allocs/op closed=%v applied=%v", got[0], got[1])
		require.Greater(t, got[1], got[0])
	})
}

// Gemini parses its body only for the anchor, so scope is decided first.
func TestL04GeminiOutOfScopeDecidedWithoutBody(t *testing.T) {
	for _, trace := range []bool{true, false} {
		s, c, k, _ := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, trace)
		k.Group.Platform = PlatformGemini
		state := *s.providerTraceEngine.cachePolicies.Snapshot()
		require.False(t, s.CacheAnchorOutOfScope(c, k, PlatformGemini), "gemini_anchor=true is in scope")
		state.anchorPolicy.gemini = false
		closed := state
		closed.master = false
		c.Set(cachePolicySnapshotGinKey, &state)
		require.True(t, s.CacheAnchorEnabled(c), "Anthropic groups keep the policy enabled")
		require.True(t, s.CacheAnchorOutOfScope(c, k, PlatformGemini))
		if trace {
			require.Equal(t, "not_in_group", cacheTraceFromGin(c).data.L04Skip)
			continue
		}
		got := cacheArmAllocs([]func(){
			func() { c.Set(cachePolicySnapshotGinKey, &closed) },
			func() { c.Set(cachePolicySnapshotGinKey, &state) },
		}, func() {
			if !s.CacheAnchorOutOfScope(c, k, PlatformGemini) {
				panic("out-of-scope Gemini request reached the parse")
			}
		})
		require.Equal(t, []float64{0, 0}, got)
	}
}
