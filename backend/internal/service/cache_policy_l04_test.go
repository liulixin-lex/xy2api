//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCachePolicyL04DefaultsDoNotRewriteStorage(t *testing.T) {
	s, store, groups := m4PolicyFixture(t, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true}), WithCacheAnthropicObservability())
	stored := `{"rev":7,"spec":{"anchor_group_ids":[31],"gemini_anchor":false,"metadata_group_ids":[],"rollout_percent":50}}`
	store.rows["cache_anthropic_identity_policy"] = stored
	require.NoError(t, s.Refresh(context.Background()))
	require.Zero(t, groups.calls)
	v, err := s.Get(context.Background(), "anthropic_identity")
	require.NoError(t, err)
	require.Equal(t, stored, store.rows["cache_anthropic_identity_policy"])
	require.Equal(t, int64(7), v.Stored.Rev)
	raw, err := json.Marshal(v.Stored.Spec)
	require.NoError(t, err)
	var spec map[string]any
	require.NoError(t, json.Unmarshal(raw, &spec))
	require.Equal(t, float64(0), spec["metadata_rollout_percent"])
	require.Equal(t, float64(2), spec["max_inflight_per_anchor"])
	require.Equal(t, "on", v.Effective.State)
}

func TestCachePolicyL04IntegerLimitsAndRequestedRollout(t *testing.T) {
	s, store, _ := m4PolicyFixture(t, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true}), WithCacheAnthropicObservability())
	store.rows["cache_anthropic_identity_policy"] = `{"rev":1,"spec":{"anchor_group_ids":[31],"rollout_percent":50,"metadata_rollout_percent":20,"max_inflight_per_anchor":2}}`
	require.NoError(t, s.Refresh(context.Background()))
	for _, tc := range []struct {
		patch, field, code string
		limit              map[string]int
	}{
		{`{"max_inflight_per_anchor":0}`, "max_inflight_per_anchor", "out_of_range", map[string]int{"min": 1, "max": 16}},
		{`{"max_inflight_per_anchor":17}`, "max_inflight_per_anchor", "out_of_range", map[string]int{"min": 1, "max": 16}},
		{`{"metadata_rollout_percent":-1}`, "metadata_rollout_percent", "out_of_range", map[string]int{"min": 0, "max": 100}},
		{`{"metadata_rollout_percent":101}`, "metadata_rollout_percent", "out_of_range", map[string]int{"min": 0, "max": 100}},
		{`{"metadata_rollout_percent":60}`, "metadata_rollout_percent", "exceeds_rollout_percent", map[string]int{"max": 50}},
		{`{"rollout_percent":10}`, "metadata_rollout_percent", "exceeds_rollout_percent", map[string]int{"max": 10}},
		{`{"rollout_percent":70,"metadata_rollout_percent":80}`, "metadata_rollout_percent", "exceeds_rollout_percent", map[string]int{"max": 70}},
	} {
		t.Run(tc.patch, func(t *testing.T) {
			v, err := s.Validate(context.Background(), "anthropic_identity", policyWrite(t, fmt.Sprintf(`{"expected_rev":1,"spec":%s}`, tc.patch)), nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.Contains(t, v.Errors, CachePolicyFieldError{Field: tc.field, Code: tc.code, Limit: tc.limit})
			for _, diff := range v.Diff {
				require.NotEqual(t, tc.field, diff.Field, "invalid field must be removed from diff")
			}
		})
	}
	// These fields are stored even without L03; an enabled metadata behavior
	// remains unsupported. The limit uses the requested rollout, not the old one.
	v, err := s.Validate(context.Background(), "anthropic_identity", policyWrite(t, `{"expected_rev":1,"spec":{"rollout_percent":80,"metadata_rollout_percent":70,"max_inflight_per_anchor":16}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "enable", v.Direction)
	require.True(t, v.Confirm.ReasonRequired)
	require.True(t, v.Confirm.CheckboxRequired)
}

func TestCachePolicyL04TighteningNeedsNoGroupQuery(t *testing.T) {
	s, store, groups := m4PolicyFixture(t, WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true}), WithCacheAnthropicObservability())
	store.rows["cache_anthropic_identity_policy"] = `{"rev":1,"spec":{"anchor_group_ids":[31],"rollout_percent":50,"metadata_rollout_percent":20,"max_inflight_per_anchor":4}}`
	require.NoError(t, s.Refresh(context.Background()))
	groups.forbid = true
	v, err := s.Validate(context.Background(), "anthropic_identity", policyWrite(t, `{"expected_rev":1,"spec":{"metadata_rollout_percent":0,"max_inflight_per_anchor":2}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "disable", v.Direction)
	require.False(t, v.Confirm.ReasonRequired)
	require.Zero(t, groups.calls)
}
