//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheProviderPolicyLegacyReadAndPartialWrite(t *testing.T) {
	ctx := context.Background()
	store := newPolicyTestStore()
	raw := `{"rev":3,"spec":{"enabled":true,"chain_percent":5,"rate_mib_per_s":32}}`
	store.rows[cachePolicySettings[1]] = raw
	svc := NewCachePolicyService(store, policyTestConfig(), WithProviderCacheTrace())
	view, err := svc.Get(ctx, "observability")
	require.NoError(t, err)
	require.Equal(t, "on", view.Effective.State)
	require.EqualValues(t, 3, view.Stored.Rev)
	require.Equal(t, 5, policyInt(view.Stored.Spec["ingress_chain_percent"]))
	require.Equal(t, 10, policyInt(view.Defaults["ingress_chain_percent"]))
	require.Equal(t, raw, store.rows[cachePolicySettings[1]], "loading must not write or change rev")

	write := policyWrite(t, `{"expected_rev":3,"spec":{"rate_mib_per_s":16}}`)
	validation, err := svc.Validate(ctx, "observability", write, nil)
	require.NoError(t, err)
	require.True(t, validation.Valid, "%+v", validation.Errors)
	require.Equal(t, []CachePolicyDiff{{"rate_mib_per_s", 32, 16}}, validation.Diff)
	updated, diff, err := svc.Update(ctx, "observability", write, nil, CachePolicyActor{})
	require.NoError(t, err)
	require.EqualValues(t, 4, updated.Stored.Rev)
	require.Equal(t, validation.Diff, diff)
	require.Equal(t, 5, policyInt(updated.Stored.Spec["ingress_chain_percent"]))

	validation, err = svc.Validate(ctx, "observability", policyWrite(t, `{"expected_rev":4,"spec":{"chain_percent":4}}`), nil)
	require.NoError(t, err)
	require.False(t, validation.Valid)
	require.Equal(t, []CachePolicyFieldError{{Field: "ingress_chain_percent", Code: "exceeds_chain_percent", Limit: map[string]int{"max": 4}}}, validation.Errors)
}

func TestCacheProviderPolicyPlatformSnapshots(t *testing.T) {
	ctx := context.Background()
	svc := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithProviderCacheTrace())
	for _, platform := range []string{"openai", "anthropic", "gemini", "messages_bridge"} {
		require.False(t, svc.Snapshot().TraceEnabledFor(platform))
	}
	_, _, err := svc.Update(ctx, "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true,"platforms":["anthropic","gemini"]}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	old := svc.Snapshot()
	require.False(t, old.TraceEnabledFor("openai"))
	require.True(t, old.TraceEnabledFor("anthropic"))
	require.True(t, old.TraceEnabledFor("gemini"))
	require.False(t, old.TraceEnabledFor("messages_bridge"))
	require.False(t, old.TraceEnabledFor("unknown"))
	require.Equal(t, 10, old.IngressChainPercent())
	require.Zero(t, testing.AllocsPerRun(100, func() {
		if !old.TraceEnabledFor("anthropic") || old.TraceEnabledFor("openai") {
			panic("unexpected frozen policy")
		}
	}))
	_, _, err = svc.Update(ctx, "observability", policyWrite(t, `{"expected_rev":1,"spec":{"platforms":["messages_bridge"]}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, old.TraceEnabledFor("anthropic"))
	require.False(t, svc.Snapshot().TraceEnabledFor("anthropic"))
	require.True(t, svc.Snapshot().TraceEnabledFor("messages_bridge"))
}

func TestCacheProviderPolicyValidation(t *testing.T) {
	svc := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithProviderCacheTrace())
	for _, tc := range []struct {
		name, raw, code string
	}{
		{"empty", `{"platforms":[]}`, "empty"},
		{"option", `{"platforms":["grok"]}`, "invalid_option"},
		{"duplicate", `{"platforms":["openai","openai"]}`, "duplicate"},
		{"type", `{"platforms":[1]}`, "malformed_body"},
		{"null", `{"platforms":null}`, "malformed_body"},
		{"range", `{"ingress_chain_percent":101}`, "out_of_range"},
		{"cross", `{"chain_percent":50,"ingress_chain_percent":60}`, "exceeds_chain_percent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := svc.Validate(context.Background(), "observability", CachePolicyWrite{Spec: json.RawMessage(tc.raw)}, nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.Len(t, v.Errors, 1)
			require.Equal(t, tc.code, v.Errors[0].Code)
			for _, diff := range v.Diff {
				require.NotEqual(t, v.Errors[0].Field, diff.Field)
			}
			if tc.name == "cross" {
				require.Equal(t, []CachePolicyDiff{{"chain_percent", 100, 50}}, v.Diff)
				require.Equal(t, "disable", v.Direction)
			}
		})
	}
}

func TestCacheProviderPolicyBuildBoundaries(t *testing.T) {
	svc := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	v, err := svc.Validate(context.Background(), "observability", policyWrite(t, `{"spec":{"platforms":["anthropic"]}}`), nil)
	require.NoError(t, err)
	require.False(t, v.Valid)
	require.Equal(t, "unsupported_in_build", v.Errors[0].Code)
	require.NotContains(t, svc.defaults(1), "platforms")
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = true
	svc = NewCachePolicyService(newPolicyTestStore(), cfg, WithProviderCacheTrace())
	view, _, err := svc.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true,"platforms":["anthropic"]}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, "killed", view.Effective.State)
	require.False(t, svc.Snapshot().TraceEnabledFor("anthropic"))
}
