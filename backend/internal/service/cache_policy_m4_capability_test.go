//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCachePolicyM4BuildCapabilitiesRejectOnlyUnsupportedEnable(t *testing.T) {
	for _, tc := range []struct {
		name, key, before, patch, field string
		capabilities                    CacheM4BuildCapabilities
	}{
		{name: "anchor", key: "anthropic_identity", patch: `{"anchor_group_ids":[31]}`, field: "anchor_group_ids"},
		{name: "gemini", key: "anthropic_identity", patch: `{"gemini_anchor":true}`, field: "gemini_anchor"},
		{name: "metadata_independent_of_l04", key: "anthropic_identity", before: `{"anchor_group_ids":[31]}`, patch: `{"metadata_group_ids":[31]}`, field: "metadata_group_ids", capabilities: CacheM4BuildCapabilities{L04: true}},
		{name: "busy_enable", key: "routing", patch: `{"keep_sticky_on_busy":{"enabled":true}}`, field: "keep_sticky_on_busy.enabled"},
		{name: "busy_scope_expand", key: "routing", before: `{"keep_sticky_on_busy":{"group_ids":[31]}}`, patch: `{"keep_sticky_on_busy":{"group_ids":[31,32]}}`, field: "keep_sticky_on_busy.group_ids"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, groups := m4PolicyFixture(t, WithCacheM4BuildCapabilities(tc.capabilities))
			setting := "cache_" + tc.key + "_policy"
			if tc.before != "" {
				store.rows[setting] = `{"rev":0,"spec":` + tc.before + `}`
			}
			require.NoError(t, s.Refresh(context.Background()))
			before := store.rows[setting]
			groups.forbid = true
			w := policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":%s}`, tc.patch))
			v, err := s.Validate(context.Background(), tc.key, w, nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.Equal(t, []CachePolicyFieldError{{Field: tc.field, Code: "unsupported_in_build"}}, v.Errors)
			require.Nil(t, v.EffectivePreview)
			require.Empty(t, v.Affected)
			require.Empty(t, v.Warnings)
			_, _, err = s.Update(context.Background(), tc.key, w, nil, CachePolicyActor{})
			var policyErr *CachePolicyError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, 400, policyErr.Status)
			require.Equal(t, "CACHE_POLICY_UNSUPPORTED", policyErr.Reason)
			var gotErrors []CachePolicyFieldError
			require.NoError(t, json.Unmarshal([]byte(policyErr.Metadata["errors"]), &gotErrors))
			require.Equal(t, v.Errors, gotErrors)
			require.Equal(t, before, store.rows[setting], "unsupported configuration is never persisted")
			require.Zero(t, groups.calls)
		})
	}
}

func TestCachePolicyM4BuildCapabilitiesPermitIndependentImplementedBehavior(t *testing.T) {
	for _, tc := range []struct {
		name, key, before, patch string
		capabilities             CacheM4BuildCapabilities
	}{
		{"l04", "anthropic_identity", `{}`, `{"anchor_group_ids":[31],"gemini_anchor":true,"rollout_percent":10}`, CacheM4BuildCapabilities{L04: true}},
		{"l03", "anthropic_identity", `{"anchor_group_ids":[31]}`, `{"metadata_group_ids":[31]}`, CacheM4BuildCapabilities{L04: true, L03: true}},
		{"l07", "routing", `{}`, `{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":10}}`, CacheM4BuildCapabilities{L07: true}},
		{"rollout_is_not_capability", "anthropic_identity", `{}`, `{"rollout_percent":10}`, CacheM4BuildCapabilities{}},
		{"busy_rollout_is_not_capability", "routing", `{}`, `{"keep_sticky_on_busy":{"rollout_percent":10}}`, CacheM4BuildCapabilities{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, _ := m4PolicyFixture(t, WithCacheM4BuildCapabilities(tc.capabilities))
			store.rows["cache_"+tc.key+"_policy"] = `{"rev":0,"spec":` + tc.before + `}`
			require.NoError(t, s.Refresh(context.Background()))
			v, err := s.Validate(context.Background(), tc.key, policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":%s}`, tc.patch)), nil)
			require.NoError(t, err)
			require.True(t, v.Valid, "%+v", v.Errors)
		})
	}
}

func TestCachePolicyM4BuildCapabilitiesKeepRollbackAvailable(t *testing.T) {
	for _, tc := range []struct{ key, before, patch, state string }{
		{"anthropic_identity", `{"anchor_group_ids":[31,999],"metadata_group_ids":[999],"gemini_anchor":true,"rollout_percent":20}`, `{"anchor_group_ids":[],"metadata_group_ids":[],"gemini_anchor":false,"rollout_percent":0}`, "off"},
		{"anthropic_identity", `{"anchor_group_ids":[31,999],"rollout_percent":20}`, `{"anchor_group_ids":[999],"rollout_percent":10}`, "blocked"},
		{"routing", `{"keep_sticky_on_busy":{"enabled":true,"group_ids":[999],"rollout_percent":20}}`, `{"keep_sticky_on_busy":{"enabled":false,"group_ids":[999],"rollout_percent":0},"affinity_mode":"score"}`, "off"},
		{"routing", `{"keep_sticky_on_busy":{"enabled":true,"group_ids":[999],"rollout_percent":20}}`, `{"keep_sticky_on_busy":{"enabled":false,"group_ids":[],"rollout_percent":0},"affinity_mode":"score"}`, "off"},
		{"routing", `{"keep_sticky_on_busy":{"enabled":true,"group_ids":[],"rollout_percent":20}}`, `{"keep_sticky_on_busy":{"group_ids":[999]}}`, "blocked"},
	} {
		t.Run(tc.key+tc.patch, func(t *testing.T) {
			s, store, groups := m4PolicyFixture(t, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{}))
			store.rows["cache_"+tc.key+"_policy"] = `{"rev":1,"spec":` + tc.before + `}`
			require.NoError(t, s.Refresh(context.Background()))
			groups.forbid = true
			w := policyWrite(t, fmt.Sprintf(`{"expected_rev":1,"spec":%s}`, tc.patch))
			v, err := s.Validate(context.Background(), tc.key, w, nil)
			require.NoError(t, err)
			require.True(t, v.Valid)
			require.Equal(t, "disable", v.Direction)
			view, _, err := s.Update(context.Background(), tc.key, w, nil, CachePolicyActor{})
			require.NoError(t, err)
			require.Equal(t, tc.state, view.Effective.State)
			require.False(t, *view.Effective.Active)
			require.Contains(t, view.Effective.Warnings, cachePolicyGroupOptionsUnavailable)
			require.Zero(t, groups.calls)
		})
	}
}

func TestCachePolicyM4DefaultWiringNeverReportsUnimplementedActive(t *testing.T) {
	for _, master := range []bool{false, true} {
		for _, tc := range []struct{ key, spec string }{
			{"anthropic_identity", `{"anchor_group_ids":[31],"rollout_percent":50}`},
			{"anthropic_identity", `{"gemini_anchor":true,"rollout_percent":50}`},
			{"routing", `{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":50}}`},
		} {
			t.Run(fmt.Sprint(master)+tc.key+tc.spec, func(t *testing.T) {
				store := newPolicyTestStore()
				store.rows["cache_feature_master"] = fmt.Sprintf(`{"rev":1,"spec":{"enabled":%t}}`, master)
				store.rows["cache_"+tc.key+"_policy"] = `{"rev":1,"spec":` + tc.spec + `}`
				// Use the production constructor, rather than the full-build test fixture.
				s := ProvideCachePolicyService(store, policyTestConfig(), nil, nil)
				t.Cleanup(s.Stop)
				require.Equal(t, CacheM4BuildCapabilities{L04: true, L03: true}, s.m4Behaviors)
				require.NoError(t, s.Refresh(context.Background()))
				view, err := s.Get(context.Background(), tc.key)
				require.NoError(t, err)
				if tc.key == "routing" {
					require.Equal(t, "blocked", view.Effective.State)
					require.False(t, *view.Effective.Active)
					require.Equal(t, []string{"not_implemented_in_build"}, view.Effective.Blockers)
				} else {
					require.Equal(t, master, *view.Effective.Active)
					require.Empty(t, view.Effective.Blockers)
					state := "masked_by_master"
					if master {
						state = "on"
					}
					require.Equal(t, state, view.Effective.State)
				}
				require.NotContains(t, view.Effective.Warnings, "anthropic_observability_missing")
			})
		}
	}
}

func TestCachePolicyM4PartialBuildUsesImplementedBehavior(t *testing.T) {
	for _, master := range []bool{false, true} {
		s, store, _ := m4PolicyFixture(t, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true}))
		store.rows["cache_feature_master"] = fmt.Sprintf(`{"rev":1,"spec":{"enabled":%t}}`, master)
		store.rows["cache_anthropic_identity_policy"] = `{"rev":1,"spec":{"anchor_group_ids":[31],"metadata_group_ids":[31],"rollout_percent":20}}`
		require.NoError(t, s.Refresh(context.Background()))
		view, err := s.Get(context.Background(), "anthropic_identity")
		require.NoError(t, err)
		state := "masked_by_master"
		if master {
			state = "on"
		}
		require.Equal(t, state, view.Effective.State)
		require.Equal(t, master, *view.Effective.Active)
		require.Empty(t, view.Effective.Blockers)
		for _, field := range view.Schema {
			marked, exists := field["not_implemented_in_build"]
			if field["field"] == "metadata_group_ids" || field["field"] == "metadata_account_ids" {
				require.True(t, exists)
				require.Equal(t, true, marked)
			} else {
				require.False(t, exists)
			}
		}
	}
	s, _, _ := m4PolicyFixture(t, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L03: true}))
	require.False(t, s.m4Behaviors.L03, "L-03 cannot be declared without its L-04 dependency")
}
