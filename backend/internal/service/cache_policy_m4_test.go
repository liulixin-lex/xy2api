//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type m4PolicyGroups struct {
	mu       sync.Mutex
	rows     []Group
	calls    int
	failPage int
	forbid   bool
}

func (g *m4PolicyGroups) ListWithFilters(_ context.Context, p pagination.PaginationParams, _, _, _ string, _ *bool) ([]Group, *pagination.PaginationResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.forbid {
		panic("disable must not read group repository")
	}
	if p.Page == g.failPage {
		return nil, nil, errors.New("GROUP_DB_SECRET_ERROR_MARKER")
	}
	start := min((p.Page-1)*p.PageSize, len(g.rows))
	end := min(start+p.PageSize, len(g.rows))
	return append([]Group{}, g.rows[start:end]...), &pagination.PaginationResult{Total: int64(len(g.rows))}, nil
}

func m4PolicyFixture(t *testing.T, extra ...CachePolicyOption) (*CachePolicyService, *policyTestStore, *m4PolicyGroups) {
	t.Helper()
	fallback := int64(77)
	groups := &m4PolicyGroups{rows: []Group{
		{ID: 18, Name: "openai", Platform: PlatformOpenAI},
		{ID: 31, Name: "claude-pool-a", Platform: PlatformAnthropic, AvailabilityFallbackGroupID: &fallback},
		{ID: 32, Name: "claude-pool-b", Platform: PlatformAnthropic, Status: "disabled"},
		{ID: 77, Name: "claude-standby", Platform: PlatformAnthropic},
	}}
	store := newPolicyTestStore()
	store.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
	// Existing contract/behavior tests model a complete M4 build. Capability
	// boundary tests explicitly replace this option with a partial/zero set.
	options := append([]CachePolicyOption{WithCacheNorthstar(), WithCacheM4Policies(groups), WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true, L03: true, L07: true})}, extra...)
	s := NewCachePolicyService(store, policyTestConfig(), options...)
	require.NoError(t, s.Refresh(context.Background()))
	require.Zero(t, groups.calls, "construction and snapshot publication never consult groups")
	return s, store, groups
}

func TestCachePolicyM4LeafDiffAndDirection(t *testing.T) {
	s, store, groups := m4PolicyFixture(t)
	store.rows["cache_routing_policy"] = `{"rev":1,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":20},"affinity_mode":"score"}}`
	require.NoError(t, s.Refresh(context.Background()))
	groups.forbid = true
	for _, raw := range []string{
		`{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"enabled":false}}}`,
		`{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"enabled":false,"group_ids":[31],"rollout_percent":20}}}`,
	} {
		v, err := s.Validate(context.Background(), "routing", policyWrite(t, raw), nil)
		require.NoError(t, err)
		require.True(t, v.Valid)
		require.Equal(t, "disable", v.Direction)
		require.Equal(t, []CachePolicyDiff{{Field: "keep_sticky_on_busy.enabled", Before: true, After: false}}, v.Diff)
		require.Nil(t, v.Confirm.TypeToConfirm)
		require.False(t, v.Confirm.ReasonRequired)
		require.Equal(t, "off", v.EffectivePreview.State)
	}
	for _, tc := range []struct {
		field         string
		before, after []int64
		want          string
	}{
		{"anchor_group_ids", []int64{}, []int64{31}, "enable"},
		{"metadata_group_ids", []int64{}, []int64{31}, "enable"},
		{"anchor_group_ids", []int64{31}, []int64{}, "disable"},
		{"metadata_group_ids", []int64{31}, []int64{32}, "mixed"},
		{"keep_sticky_on_busy.group_ids", []int64{}, []int64{31}, "disable"},
		{"keep_sticky_on_busy.group_ids", []int64{31}, []int64{}, "enable"},
		{"account_ids", []int64{}, []int64{101}, "disable"},
	} {
		require.Equal(t, tc.want, policyDirection([]CachePolicyDiff{{Field: tc.field, Before: tc.before, After: tc.after}}))
	}
	require.Zero(t, groups.calls)
}

func TestCachePolicyM4ValidationAndUnsupported(t *testing.T) {
	s, _, _ := m4PolicyFixture(t)
	for _, tc := range []struct{ key, patch, field, code string }{
		{"anthropic_identity", `{"anchor_group_ids":[18,999]}`, "anchor_group_ids", "wrong_platform"},
		{"anthropic_identity", `{"anchor_group_ids":[31],"metadata_group_ids":[32]}`, "metadata_group_ids", "not_subset_of_anchor_groups"},
		{"anthropic_identity", `{"anchor_group_ids":[31,31]}`, "anchor_group_ids", "duplicate"},
		{"anthropic_identity", `{"anchor_group_ids":null}`, "anchor_group_ids", "malformed_body"},
		{"anthropic_identity", `{"rollout_percent":101}`, "rollout_percent", "out_of_range"},
		{"routing", `{"affinity_mode":"prev_host"}`, "affinity_mode", "unsupported_in_build"},
		{"routing", `{"affinity_mode":"prev_host+quality"}`, "affinity_mode", "unsupported_in_build"},
		{"routing", `{"affinity_mode":"anything"}`, "affinity_mode", "unsupported_in_build"},
		{"routing", `{"keep_sticky_on_busy":null}`, "keep_sticky_on_busy", "malformed_body"},
		{"routing", `{"keep_sticky_on_busy.enabled":true}`, "keep_sticky_on_busy.enabled", "unknown_field"},
		{"routing", `{"keep_sticky_on_busy":{"enabled":false},"keep_sticky_on_busy.enabled":true}`, "keep_sticky_on_busy.enabled", "unknown_field"},
		{"routing", `{"keep_sticky_on_busy":{"rollout_percent":-1}}`, "keep_sticky_on_busy.rollout_percent", "out_of_range"},
		{"routing", `{"keep_sticky_on_busy":{"secret":true}}`, "keep_sticky_on_busy.secret", "unknown_field"},
	} {
		t.Run(tc.key+"/"+tc.patch, func(t *testing.T) {
			w := policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":%s}`, tc.patch))
			v, err := s.Validate(context.Background(), tc.key, w, nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.Equal(t, tc.field, v.Errors[0].Field)
			require.Equal(t, tc.code, v.Errors[0].Code)
			require.Nil(t, v.EffectivePreview)
			require.Empty(t, v.Warnings)
			require.Empty(t, v.Affected)
			_, _, err = s.Update(context.Background(), tc.key, w, nil, CachePolicyActor{})
			var policyErr *CachePolicyError
			require.ErrorAs(t, err, &policyErr)
			if tc.code == "unsupported_in_build" {
				require.Equal(t, "CACHE_POLICY_UNSUPPORTED", policyErr.Reason)
			} else {
				require.Equal(t, "CACHE_POLICY_INVALID", policyErr.Reason)
			}
		})
	}
}

func TestCachePolicyM4TypedConfirmationAndAuditValue(t *testing.T) {
	s, store, groups := m4PolicyFixture(t)
	for _, typed := range []string{"null", `"routing"`, `"cache_routing_policy "`} {
		w := policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":20}},"reason":"enable","confirm":{"checkbox":true,"typed":%s}}`, typed))
		_, _, err := s.Update(context.Background(), "routing", w, nil, CachePolicyActor{})
		var e *CachePolicyError
		require.ErrorAs(t, err, &e)
		require.Equal(t, 422, e.Status)
		require.Equal(t, map[string]string{"missing": "typed", "type_to_confirm": "cache_routing_policy"}, e.Metadata)
		require.Empty(t, store.rows["cache_routing_policy"])
	}
	w := policyWrite(t, `{"expected_rev":0,"spec":{"keep_sticky_on_busy":{"enabled":true,"rollout_percent":20}},"reason":"enable","confirm":{"checkbox":true,"typed":"cache_routing_policy"}}`)
	view, _, err := s.Update(context.Background(), "routing", w, nil, CachePolicyActor{UserID: 2, Email: "oncall@example.com"})
	require.NoError(t, err)
	require.Equal(t, "on", view.Effective.State)
	groups.forbid = true
	view, _, err = s.Update(context.Background(), "routing", policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"enabled":false}}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Nil(t, view.Stored.Reason)
	require.Equal(t, []string{cachePolicyGroupOptionsUnavailable}, view.Effective.Warnings)
}

func TestCachePolicyM4DisableNeverReadsOrReusesGroupFacts(t *testing.T) {
	s, store, groups := m4PolicyFixture(t)
	store.rows["cache_anthropic_identity_policy"] = `{"rev":2,"spec":{"anchor_group_ids":[31,77,999],"metadata_group_ids":[],"rollout_percent":50}}`
	require.NoError(t, s.Refresh(context.Background()))
	view, err := s.Get(context.Background(), "anthropic_identity")
	require.NoError(t, err)
	require.Contains(t, view.Effective.Warnings, "fallback_group_selected")
	before := groups.calls
	groups.forbid = true
	w := policyWrite(t, `{"expected_rev":2,"spec":{"anchor_group_ids":[77,999]}}`)
	v, err := s.Validate(context.Background(), "anthropic_identity", w, nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "disable", v.Direction)
	require.Equal(t, []string{"anthropic_observability_missing"}, v.Warnings)
	view, _, err = s.Update(context.Background(), "anthropic_identity", w, nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic_observability_missing", cachePolicyGroupOptionsUnavailable}, view.Effective.Warnings)
	require.Equal(t, before, groups.calls)
}

func TestCachePolicyM4ObservabilityCapabilityStates(t *testing.T) {
	for _, master := range []bool{false, true} {
		for _, covered := range []bool{false, true} {
			for _, percent := range []int{0, 50} {
				var options []CachePolicyOption
				if covered {
					options = append(options, WithCacheAnthropicObservability())
				}
				s, store, _ := m4PolicyFixture(t, options...)
				store.rows["cache_feature_master"] = fmt.Sprintf(`{"rev":2,"spec":{"enabled":%t}}`, master)
				store.rows["cache_anthropic_identity_policy"] = fmt.Sprintf(`{"rev":1,"spec":{"anchor_group_ids":[31],"rollout_percent":%d}}`, percent)
				require.NoError(t, s.Refresh(context.Background()))
				v, err := s.Get(context.Background(), "anthropic_identity")
				require.NoError(t, err)
				want := "off"
				if percent > 0 {
					if master {
						want = "on"
					} else {
						want = "masked_by_master"
					}
				}
				require.Equal(t, want, v.Effective.State)
				if !covered && percent > 0 {
					require.Contains(t, v.Effective.Warnings, "anthropic_observability_missing")
				} else {
					require.NotContains(t, v.Effective.Warnings, "anthropic_observability_missing")
				}
			}
		}
	}
	s, store, _ := m4PolicyFixture(t)
	store.rows["cache_anthropic_identity_policy"] = `{"rev":1,"spec":{"anchor_group_ids":[77],"rollout_percent":50}}`
	require.NoError(t, s.Refresh(context.Background()))
	v, err := s.Get(context.Background(), "anthropic_identity")
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic_observability_missing", "fallback_group_selected"}, v.Effective.Warnings)
	store.rows["cache_anthropic_identity_policy"] = `{"rev":2,"spec":{"anchor_group_ids":[77],"rollout_percent":0}}`
	require.NoError(t, s.Refresh(context.Background()))
	v, err = s.Get(context.Background(), "anthropic_identity")
	require.NoError(t, err)
	require.Equal(t, []string{"fallback_group_selected", "no_effect_rollout_zero"}, v.Effective.Warnings)

}

func TestCachePolicyM4GroupCatalogLimitsAndFailure(t *testing.T) {
	s, _, groups := m4PolicyFixture(t)
	groups.rows = nil
	for id := int64(600); id > 0; id-- {
		platform := PlatformAnthropic
		if id%10 == 0 {
			platform = PlatformOpenAI
		}
		groups.rows = append(groups.rows, Group{ID: id, Name: fmt.Sprint(id), Platform: platform})
	}
	list, err := s.List(context.Background(), "unit")
	require.NoError(t, err)
	for _, index := range []int{4, 5} {
		for _, schema := range list.Policies[index].Schema {
			if schema["ref"] != "group" {
				continue
			}
			options := schema["options"].([]cachePolicyAccountOption)
			require.Len(t, options, 500)
			for j, opt := range options {
				require.Equal(t, PlatformAnthropic, opt.Platform)
				if j > 0 {
					require.Less(t, options[j-1].ID, opt.ID)
				}
			}
		}
	}
	for _, schema := range list.Policies[2].Schema {
		_, has := schema["options"]
		if schema["field"] == "excluded_group_ids" {
			require.False(t, has)
		}
	}
	groups.failPage = 2
	list, err = s.List(context.Background(), "unit")
	require.NoError(t, err)
	for _, index := range []int{4, 5} {
		v := list.Policies[index]
		require.Equal(t, cachePolicyGroupOptionsUnavailable, v.Effective.Warnings[len(v.Effective.Warnings)-1])
		for _, schema := range v.Schema {
			if schema["ref"] == "group" {
				require.Empty(t, schema["options"])
			}
		}
	}
}

func TestCachePolicyM4NestedSnapshotsAreImmutable(t *testing.T) {
	s, store, groups := m4PolicyFixture(t)
	store.rows["cache_routing_policy"] = `{"rev":1,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":20}}}`
	require.NoError(t, s.Refresh(context.Background()))
	old := s.Snapshot()
	v, err := s.Get(context.Background(), "routing")
	require.NoError(t, err)
	nested := v.Stored.Spec["keep_sticky_on_busy"].(map[string]any)
	nested["enabled"] = false
	nested["group_ids"].([]int64)[0] = 77
	require.Equal(t, true, m4PolicyField(old.entries[5].envelope.Spec, "keep_sticky_on_busy.enabled"))
	require.Equal(t, []int64{31}, m4PolicyField(old.entries[5].envelope.Spec, "keep_sticky_on_busy.group_ids"))
	groups.forbid = true
	_, _, err = s.Update(context.Background(), "routing", policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"enabled":false}}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, true, m4PolicyField(old.entries[5].envelope.Spec, "keep_sticky_on_busy.enabled"))
	require.Equal(t, false, m4PolicyField(s.Snapshot().entries[5].envelope.Spec, "keep_sticky_on_busy.enabled"))
}
