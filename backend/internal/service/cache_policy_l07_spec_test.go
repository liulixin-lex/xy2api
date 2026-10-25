//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// The routing card in a T2.5 build: complete M4 storage with L-07 implemented.
func l07PolicyFixture(t *testing.T, cfg *config.Config, extra ...CachePolicyOption) (*CachePolicyService, *policyTestStore, *m4PolicyGroups) {
	t.Helper()
	fallback := int64(77)
	groups := &m4PolicyGroups{rows: []Group{
		{ID: 31, Name: "claude-pool-a", Platform: PlatformAnthropic, AvailabilityFallbackGroupID: &fallback},
		{ID: 32, Name: "claude-pool-b", Platform: PlatformAnthropic},
		{ID: 77, Name: "claude-standby", Platform: PlatformAnthropic},
	}}
	store := newPolicyTestStore()
	store.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
	if cfg == nil {
		cfg = policyTestConfig()
	}
	// The production build: T2.5 and T3.1a are implemented.
	options := append([]CachePolicyOption{WithCacheNorthstar(), WithCacheM4Policies(groups), WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L07: true, L09A: true})}, extra...)
	s := NewCachePolicyService(store, cfg, options...)
	require.NoError(t, s.Refresh(context.Background()))
	return s, store, groups
}

const l07LegacyRouting = `{"rev":1,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":20},"affinity_mode":"score"}}`

func l07Busy(t *testing.T, spec map[string]any) map[string]any {
	t.Helper()
	busy, ok := spec["keep_sticky_on_busy"].(map[string]any)
	require.True(t, ok)
	return busy
}

func TestCacheL07PolicyDefaultsAndSchema(t *testing.T) {
	s, _, _ := l07PolicyFixture(t, nil)
	want := map[string]any{"enabled": false, "group_ids": []int64{}, "rollout_percent": 0, "max_consecutive_busy": 2,
		"max_hold_seconds": 60, "owner_busy_skip_per_min_cap": 0, "group_keep_ratio_pct": 5, "health_lookback_seconds": 120}
	require.Equal(t, want, l07Busy(t, s.defaults(5)))
	fields := []string{}
	for _, field := range s.schema(5) {
		fields = append(fields, fmt.Sprint(field["field"]))
		_, marked := field["not_implemented_in_build"]
		require.False(t, marked, field["field"])
	}
	require.Equal(t, []string{"keep_sticky_on_busy.enabled", "keep_sticky_on_busy.group_ids", "keep_sticky_on_busy.rollout_percent",
		"keep_sticky_on_busy.max_consecutive_busy", "keep_sticky_on_busy.max_hold_seconds", "keep_sticky_on_busy.owner_busy_skip_per_min_cap",
		"keep_sticky_on_busy.group_keep_ratio_pct", "keep_sticky_on_busy.health_lookback_seconds", "affinity_mode", "affinity_rollout_percent"}, fields)
	require.Equal(t, map[string]any{"field": "keep_sticky_on_busy.group_keep_ratio_pct", "type": "int", "min": 1, "max": 20, "unit": "percent",
		"cap_source": "gateway.cache_routing.l07_group_keep_ratio_cap_pct"}, s.schema(5)[6])
	require.Equal(t, map[string]any{"field": "keep_sticky_on_busy.owner_busy_skip_per_min_cap", "type": "int", "min": 0, "max": 1000}, s.schema(5)[5])

	// The parameters are not capability fields: a dormant build marks only
	// enabled and group_ids (and the T3.1a affinity_mode), and still stores
	// the parameters.
	dormant, store, _ := l07PolicyFixture(t, nil, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{}))
	marked := []string{}
	for _, field := range dormant.schema(5) {
		if field["not_implemented_in_build"] == true {
			marked = append(marked, fmt.Sprint(field["field"]))
		}
	}
	require.Equal(t, []string{"keep_sticky_on_busy.enabled", "keep_sticky_on_busy.group_ids", "affinity_mode"}, marked)
	w := policyWrite(t, `{"expected_rev":0,"spec":{"keep_sticky_on_busy":{"max_hold_seconds":120,"owner_busy_skip_per_min_cap":8}},"reason":"prepare","confirm":{"checkbox":true,"typed":"cache_routing_policy"}}`)
	v, err := dormant.Validate(context.Background(), "routing", w, nil)
	require.NoError(t, err)
	require.True(t, v.Valid, "%+v", v.Errors)
	require.Equal(t, "mixed", v.Direction)
	view, _, err := dormant.Update(context.Background(), "routing", w, nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, "off", view.Effective.State)
	require.Empty(t, view.Effective.Blockers)
	require.Contains(t, store.rows["cache_routing_policy"], `"max_hold_seconds":120`)
}

func TestCacheL07PolicyRangesAndStaticCaps(t *testing.T) {
	cfg := policyTestConfig()
	cfg.Gateway.CacheRouting.L07MaxHoldSecondsCap = 120
	s, _, _ := l07PolicyFixture(t, cfg)
	require.Equal(t, 120, s.schema(5)[4]["max"])
	require.Equal(t, 5, s.schema(5)[3]["max"])
	for _, tc := range []struct {
		patch string
		want  []CachePolicyFieldError
	}{
		{`{"max_consecutive_busy":0}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.max_consecutive_busy", Code: "out_of_range", Limit: map[string]int{"min": 1, "max": 5}}}},
		{`{"max_consecutive_busy":6}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.max_consecutive_busy", Code: "exceeds_static_cap", Limit: map[string]int{"max": 5}}}},
		{`{"max_hold_seconds":9}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.max_hold_seconds", Code: "out_of_range", Limit: map[string]int{"min": 10, "max": 120}}}},
		{`{"max_hold_seconds":121}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.max_hold_seconds", Code: "exceeds_static_cap", Limit: map[string]int{"max": 120}}}},
		{`{"max_hold_seconds":301}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.max_hold_seconds", Code: "exceeds_static_cap", Limit: map[string]int{"max": 120}}}},
		{`{"owner_busy_skip_per_min_cap":-1}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.owner_busy_skip_per_min_cap", Code: "out_of_range", Limit: map[string]int{"min": 0, "max": 1000}}}},
		{`{"owner_busy_skip_per_min_cap":1001}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.owner_busy_skip_per_min_cap", Code: "out_of_range", Limit: map[string]int{"min": 0, "max": 1000}}}},
		{`{"group_keep_ratio_pct":0}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.group_keep_ratio_pct", Code: "out_of_range", Limit: map[string]int{"min": 1, "max": 20}}}},
		{`{"group_keep_ratio_pct":21}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.group_keep_ratio_pct", Code: "exceeds_static_cap", Limit: map[string]int{"max": 20}}}},
		{`{"health_lookback_seconds":29}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.health_lookback_seconds", Code: "out_of_range", Limit: map[string]int{"min": 30, "max": 600}}}},
		{`{"health_lookback_seconds":601}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.health_lookback_seconds", Code: "out_of_range", Limit: map[string]int{"min": 30, "max": 600}}}},
		{`{"health_lookback_seconds":1.5}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.health_lookback_seconds", Code: "malformed_body"}}},
		{`{"max_hold_seconds":null}`, []CachePolicyFieldError{{Field: "keep_sticky_on_busy.max_hold_seconds", Code: "malformed_body"}}},
	} {
		t.Run(tc.patch, func(t *testing.T) {
			w := policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":{"keep_sticky_on_busy":%s},"reason":"r","confirm":{"checkbox":true,"typed":"cache_routing_policy"}}`, tc.patch))
			v, err := s.Validate(context.Background(), "routing", w, nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.Equal(t, tc.want, v.Errors)
			_, _, err = s.Update(context.Background(), "routing", w, nil, CachePolicyActor{})
			var policyErr *CachePolicyError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, 400, policyErr.Status)
			require.Equal(t, "CACHE_POLICY_INVALID", policyErr.Reason)
		})
	}
	for _, patch := range []string{`{"max_consecutive_busy":5}`, `{"max_hold_seconds":120}`, `{"owner_busy_skip_per_min_cap":1000}`, `{"group_keep_ratio_pct":20}`, `{"health_lookback_seconds":600}`, `{"health_lookback_seconds":30}`} {
		v, err := s.Validate(context.Background(), "routing", policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":{"keep_sticky_on_busy":%s}}`, patch)), nil)
		require.NoError(t, err)
		require.True(t, v.Valid, "%s %+v", patch, v.Errors)
	}
}

func TestCacheL07PolicyLoweredCapClipsStoredValue(t *testing.T) {
	cfg := policyTestConfig()
	cfg.Gateway.CacheRouting.L07MaxHoldSecondsCap = 120
	cfg.Gateway.CacheRouting.L07GroupKeepRatioCapPct = 10
	s, store, _ := l07PolicyFixture(t, cfg)
	store.rows["cache_routing_policy"] = `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":20,"max_consecutive_busy":5,"max_hold_seconds":300,"group_keep_ratio_pct":20}}}`
	require.NoError(t, s.Refresh(context.Background()))
	view, err := s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "on", view.Effective.State)
	require.Equal(t, []string{"keep_sticky_on_busy.max_hold_seconds", "keep_sticky_on_busy.group_keep_ratio_pct"}, view.Effective.ClippedFields)
	require.Equal(t, 300, l07Busy(t, view.Stored.Spec)["max_hold_seconds"], "the stored value is shown unchanged")
	require.False(t, s.Snapshot().entries[5].parseError)

	// Values outside the contract range never load: the key fails closed.
	for _, raw := range []string{
		`{"rev":4,"spec":{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":20,"max_consecutive_busy":6}}}`,
		`{"rev":4,"spec":{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":20,"max_hold_seconds":301}}}`,
		`{"rev":4,"spec":{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":20,"health_lookback_seconds":10}}}`,
	} {
		store.rows["cache_routing_policy"] = raw
		require.NoError(t, s.Refresh(context.Background()))
		view, err := s.Get(context.Background(), "routing")
		require.NoError(t, err)
		require.Equal(t, "parse_error", view.Effective.State, raw)
		require.Equal(t, []string{"parse_error"}, view.Effective.Blockers)
		require.Empty(t, view.Effective.ClippedFields)
	}
}

func TestCacheL07PolicyLegacyFillIsInMemoryOnly(t *testing.T) {
	s, store, _ := l07PolicyFixture(t, nil)
	store.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, s.Refresh(context.Background()))
	view, err := s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.EqualValues(t, 1, view.Stored.Rev)
	require.Equal(t, "on", view.Effective.State)
	busy := l07Busy(t, view.Stored.Spec)
	for _, f := range cacheL07Fields {
		require.Equal(t, f.def, busy[f.name], f.name)
	}
	require.Equal(t, l07LegacyRouting, store.rows["cache_routing_policy"], "reading never writes back")
	require.EqualValues(t, 1, s.Snapshot().revs[5])
	// The filled spec is the merge base of validate and PUT.
	v, err := s.Validate(context.Background(), "routing", policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"max_hold_seconds":30,"rollout_percent":20}}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, []CachePolicyDiff{{Field: "keep_sticky_on_busy.max_hold_seconds", Before: 60, After: 30}}, v.Diff)
	require.Equal(t, "disable", v.Direction)
	view, _, err = s.Update(context.Background(), "routing", policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"max_hold_seconds":30}}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.EqualValues(t, 2, view.Stored.Rev)
	var stored struct {
		Spec map[string]any `json:"spec"`
	}
	require.NoError(t, json.Unmarshal([]byte(store.rows["cache_routing_policy"]), &stored))
	require.Equal(t, map[string]any{"enabled": true, "group_ids": []any{float64(31)}, "rollout_percent": float64(20), "max_consecutive_busy": float64(2),
		"max_hold_seconds": float64(30), "owner_busy_skip_per_min_cap": float64(0), "group_keep_ratio_pct": float64(5), "health_lookback_seconds": float64(120)}, stored.Spec["keep_sticky_on_busy"])
}

func TestCacheL07PolicyDirections(t *testing.T) {
	k := cacheL07Prefix
	for _, tc := range []struct {
		field         string
		before, after int
		want          string
	}{
		{k + "max_consecutive_busy", 2, 3, "enable"},
		{k + "max_consecutive_busy", 3, 2, "disable"},
		{k + "max_hold_seconds", 60, 120, "enable"},
		{k + "max_hold_seconds", 120, 60, "disable"},
		{k + "group_keep_ratio_pct", 5, 10, "enable"},
		{k + "group_keep_ratio_pct", 10, 5, "disable"},
		{k + "owner_busy_skip_per_min_cap", 8, 16, "enable"},
		{k + "owner_busy_skip_per_min_cap", 16, 8, "disable"},
		{k + "owner_busy_skip_per_min_cap", 8, 0, "enable"},
		{k + "owner_busy_skip_per_min_cap", 0, 8, "disable"},
		{k + "owner_busy_skip_per_min_cap", 0, 1000, "disable"},
		{k + "health_lookback_seconds", 120, 300, "disable"},
		{k + "health_lookback_seconds", 120, 60, "enable"},
		{k + "rollout_percent", 0, 10, "enable"},
	} {
		require.Equal(t, tc.want, policyDirection([]CachePolicyDiff{{Field: tc.field, Before: tc.before, After: tc.after}}), "%s %d->%d", tc.field, tc.before, tc.after)
	}
	require.Equal(t, "mixed", policyDirection([]CachePolicyDiff{
		{Field: k + "health_lookback_seconds", Before: 120, After: 300},
		{Field: k + "owner_busy_skip_per_min_cap", Before: 8, After: 0},
	}))
	require.Equal(t, "disable", policyDirection([]CachePolicyDiff{
		{Field: k + "health_lookback_seconds", Before: 120, After: 300},
		{Field: k + "max_hold_seconds", Before: 60, After: 30},
	}))

	// End to end: disable never consults groups or asks for confirmation.
	s, store, groups := l07PolicyFixture(t, nil)
	store.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, s.Refresh(context.Background()))
	groups.forbid = true
	v, err := s.Validate(context.Background(), "routing", policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"health_lookback_seconds":300,"owner_busy_skip_per_min_cap":4}}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "disable", v.Direction)
	require.False(t, v.Confirm.ReasonRequired)
	require.Nil(t, v.Confirm.TypeToConfirm)
	require.Zero(t, groups.calls)
	groups.forbid = false
	v, err = s.Validate(context.Background(), "routing", policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"health_lookback_seconds":60}}}`), nil)
	require.NoError(t, err)
	require.Equal(t, "enable", v.Direction)
	require.True(t, v.Confirm.ReasonRequired)
	require.Equal(t, "cache_routing_policy", *v.Confirm.TypeToConfirm)
}

func TestCacheL07PolicyGuardTripped(t *testing.T) {
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s, store, _ := l07PolicyFixture(t, nil, WithCachePolicyClock(clock))
	store.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, s.Refresh(context.Background()))
	require.False(t, s.L07GuardTripped())
	s.l07.guardUntil.Store(now.Add(10 * time.Minute).UnixNano())
	require.True(t, s.L07GuardTripped())
	view, err := s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "blocked", view.Effective.State)
	require.False(t, *view.Effective.Active)
	require.Equal(t, []string{"guard_tripped"}, view.Effective.Blockers)
	require.Equal(t, l07LegacyRouting, store.rows["cache_routing_policy"], "the guard never changes the stored policy")

	// The guard shows blocked even with the master off.
	store.rows["cache_feature_master"] = `{"rev":2,"spec":{"enabled":false}}`
	require.NoError(t, s.Refresh(context.Background()))
	view, err = s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "blocked", view.Effective.State)
	store.rows["cache_feature_master"] = `{"rev":3,"spec":{"enabled":true}}`
	require.NoError(t, s.Refresh(context.Background()))

	// A disabled policy stays off during the pause.
	store.rows["cache_routing_policy"] = `{"rev":2,"spec":{"keep_sticky_on_busy":{"enabled":false,"group_ids":[31],"rollout_percent":20}}}`
	require.NoError(t, s.Refresh(context.Background()))
	view, err = s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "off", view.Effective.State)
	require.Empty(t, view.Effective.Blockers)

	// The pause expires on its own.
	store.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, s.Refresh(context.Background()))
	now = now.Add(10 * time.Minute)
	view, err = s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "on", view.Effective.State)
	require.True(t, *view.Effective.Active)

	// Ordering after the other blockers; the dormant build has no guard.
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = true
	killed, killedStore, _ := l07PolicyFixture(t, cfg, WithCacheL07GuardTrippedUntil(time.Now().Add(time.Hour)))
	killedStore.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, killed.Refresh(context.Background()))
	view, err = killed.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "killed", view.Effective.State)
	require.Equal(t, []string{"kill_switch", "guard_tripped"}, view.Effective.Blockers)
	dormant, dormantStore, _ := l07PolicyFixture(t, nil, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{}), WithCacheL07GuardTrippedUntil(time.Now().Add(time.Hour)))
	dormantStore.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, dormant.Refresh(context.Background()))
	view, err = dormant.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, []string{"not_implemented_in_build"}, view.Effective.Blockers)
}

func TestCacheL07PolicyShareGroupWarning(t *testing.T) {
	share := NewShareSchedulingService(nil, nil, nil)
	share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true}, groups: map[int64]*shareGroupPolicy{
		32: {groupID: 32, policy: sharesched.Policy{Scheduler: sharesched.SchedulerShare}},
		31: {groupID: 31, policy: sharesched.Policy{}},
	}})
	require.True(t, share.shareSchedulingActive(32))
	require.False(t, share.shareSchedulingActive(31))
	s, store, groups := l07PolicyFixture(t, nil)
	s.attachShareScheduling(share)
	(*CachePolicyService)(nil).attachShareScheduling(share)
	s.attachShareScheduling(nil)
	for _, tc := range []struct {
		spec string
		want []string
	}{
		{`{"enabled":true,"group_ids":[31],"rollout_percent":20}`, []string{}},
		{`{"enabled":true,"group_ids":[],"rollout_percent":20}`, []string{}},
		{`{"enabled":true,"group_ids":[31,32],"rollout_percent":20}`, []string{"share_group_no_effect"}},
		{`{"enabled":false,"group_ids":[32],"rollout_percent":0}`, []string{"share_group_no_effect"}},
		{`{"enabled":true,"group_ids":[32,77],"rollout_percent":20}`, []string{"fallback_group_selected", "share_group_no_effect"}},
		{`{"enabled":true,"group_ids":[32,77],"rollout_percent":0}`, []string{"fallback_group_selected", "no_effect_rollout_zero", "share_group_no_effect"}},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			store.rows["cache_routing_policy"] = `{"rev":1,"spec":{"keep_sticky_on_busy":` + tc.spec + `}}`
			require.NoError(t, s.Refresh(context.Background()))
			view, err := s.Get(context.Background(), "routing")
			require.NoError(t, err)
			require.Equal(t, tc.want, view.Effective.Warnings)
		})
	}
	// Option warnings stay last; the share lookup never reads a repository.
	groups.failPage = 1
	view, err := s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, []string{"no_effect_rollout_zero", "share_group_no_effect", cachePolicyGroupOptionsUnavailable}, view.Effective.Warnings)
	groups.failPage = 0

	// Validate warns and still allows the write.
	store.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, s.Refresh(context.Background()))
	w := policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"group_ids":[31,32]}},"reason":"r","confirm":{"checkbox":true,"typed":"cache_routing_policy"}}`)
	v, err := s.Validate(context.Background(), "routing", w, nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, []string{"share_group_no_effect"}, v.Warnings)
	view, _, err = s.Update(context.Background(), "routing", w, nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, []string{"share_group_no_effect"}, view.Effective.Warnings)
	// Disabling share scheduling clears the warning without touching the policy.
	share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{}, groups: share.state.Load().groups})
	view, err = s.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Empty(t, view.Effective.Warnings)

	// The option form is equivalent, for callers without the share service.
	opt, optStore, _ := l07PolicyFixture(t, nil, WithCachePolicyShareActive(func(id int64) bool { return id == 31 }))
	optStore.rows["cache_routing_policy"] = l07LegacyRouting
	require.NoError(t, opt.Refresh(context.Background()))
	view, err = opt.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, []string{"share_group_no_effect"}, view.Effective.Warnings)
}

func TestCacheL07PolicyHistoryWhitelist(t *testing.T) {
	for _, f := range cacheL07Fields {
		require.True(t, cachePolicyHistoryField("routing", cacheL07Prefix+f.name), f.name)
		require.False(t, cachePolicyHistoryField("anthropic_identity", cacheL07Prefix+f.name), f.name)
	}
}
