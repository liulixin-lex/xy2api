//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheNorthstarPolicyLifecycle(t *testing.T) {
	cfg := policyTestConfig()
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	store := newPolicyTestStore()
	policies := NewCachePolicyService(store, cfg, WithCacheNorthstar())
	peer := NewCachePolicyService(store, cfg, WithCacheNorthstar())
	before := policies.Snapshot()
	require.False(t, before.NorthstarRollupEnabled())
	view, err := policies.Get(context.Background(), "northstar")
	require.NoError(t, err)
	require.True(t, view.Available)
	require.False(t, view.GatedByMaster)
	require.Equal(t, "low", view.Risk)
	_, _, err = policies.Update(context.Background(), "northstar", policyWrite(t, `{"expected_rev":0,"spec":{"rollup_enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, policies.Snapshot().NorthstarRollupEnabled())
	require.False(t, policies.Snapshot().MasterEnabled())
	require.False(t, before.NorthstarRollupEnabled())
	require.NoError(t, peer.Refresh(context.Background()))
	require.True(t, peer.Snapshot().NorthstarRollupEnabled())
	require.EqualValues(t, 1, peer.Snapshot().NorthstarRevision())
	clone := peer.Snapshot().NorthstarPolicy()
	clone.ModelThresholds[0].Threshold = 6144
	require.NotEqual(t, clone.ModelThresholds[0], peer.Snapshot().NorthstarPolicy().ModelThresholds[0])
	cfg.Gateway.CacheFeatures.KillSwitch = true
	killed := NewCachePolicyService(store, cfg, WithCacheNorthstar())
	_, _, err = killed.Update(context.Background(), "northstar", policyWrite(t, `{"expected_rev":1,"spec":{"backfill_days":1}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.False(t, killed.Snapshot().NorthstarRollupEnabled())
	view, err = killed.Get(context.Background(), "northstar")
	require.NoError(t, err)
	require.Equal(t, "killed", view.Effective.State)
}

func TestCacheNorthstarPolicyRulesAndVersion(t *testing.T) {
	cases := []struct {
		name, spec string
		valid      bool
	}{
		{"rule requires version", `{"exclude_media":false}`, false},
		{"version and rule", `{"exclude_media":false,"policy_version":"test-v2"}`, true},
		{"rollout only", `{"rollup_enabled":true,"backfill_days":30}`, true},
		{"too much backfill", `{"backfill_days":31}`, false},
		{"duplicate ids", `{"excluded_group_ids":[1,1],"policy_version":"test-v2"}`, false},
		{"unknown field", `{"secret":"ignored"}`, false},
		{"null bool", `{"rollup_enabled":null}`, false},
		{"unknown behavior", `{"unknown_threshold":"include","policy_version":"test-v2"}`, false},
		{"invalid threshold", `{"model_thresholds":[{"match":"prefix","model":"gpt-","threshold":1000}],"policy_version":"test-v2"}`, false},
		{"valid rules", `{"model_thresholds":[{"match":"prefix","model":"model","threshold":1024},{"match":"exact","model":"model-x","threshold":4096}],"policy_version":"test-v2"}`, true},
		{"unknown nested", `{"model_thresholds":[{"match":"exact","model":"x","threshold":1024,"extra":1}],"policy_version":"test-v2"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
			body := CachePolicyWrite{Spec: json.RawMessage(tc.spec)}
			rev := int64(0)
			body.ExpectedRev = &rev
			got, err := s.Validate(context.Background(), "northstar", body, nil)
			require.NoError(t, err)
			require.Equal(t, tc.valid, got.Valid)
		})
	}
	p := CacheNorthstarPolicy{ModelThresholds: []CacheNorthstarThresholdRule{{Match: "prefix", Model: "model", Threshold: 1024}, {Match: "prefix", Model: "model-long", Threshold: 2048}, {Match: "exact", Model: "model-long-x", Threshold: 4096}}}
	threshold, known := p.Threshold("model-long-x")
	require.True(t, known)
	require.Equal(t, 4096, threshold)
	threshold, known = p.Threshold("model-long-y")
	require.True(t, known)
	require.Equal(t, 2048, threshold)
	_, known = p.Threshold("unknown")
	require.False(t, known)
}

func TestCacheNorthstarM2TracePersistencePolicy(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
	require.False(t, s.Snapshot().PersistTraceHourlyEnabled())
	_, _, err := s.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true,"persist_trace_hourly":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, s.Snapshot().TraceEnabled())
	require.False(t, s.Snapshot().PersistTraceHourlyEnabled())
	_, _, err = s.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":1,"spec":{"persist_trace_hourly":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, s.Snapshot().PersistTraceHourlyEnabled())
	_, _, err = s.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":2,"spec":{"enabled":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.False(t, s.Snapshot().PersistTraceHourlyEnabled())
}
