//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Single-policy responses are independent of the other cards' field sets.
func TestCachePolicyL04SinglePolicyContractReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/m4l04"
	manifest, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	for _, name := range []string{
		"m4l04_validate_metadata_rollout_exceeds.json",
		"m4l04_validate_metadata_dormant.json",
		"m4l04_validate_max_inflight_anchor_raise.json",
		"m4l04_put_anchor_enable_200.json",
		"m4l04_m4_put_anthropic_200.json",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var setup struct {
				Fixture struct {
					Capabilities *service.CacheM4BuildCapabilities `json:"build_capabilities"`
				} `json:"_fixture"`
			}
			require.NoError(t, json.Unmarshal(raw, &setup))
			capabilities := service.CacheM4BuildCapabilities{L04: true, L03: true, L07: true}
			if setup.Fixture.Capabilities != nil {
				capabilities = *setup.Fixture.Capabilities
				require.Equal(t, service.CacheM4BuildCapabilities{L04: true}, capabilities)
			}
			var request struct {
				ExpectedRev int64 `json:"expected_rev"`
			}
			require.NoError(t, json.Unmarshal(f.Request.Body, &request))
			before := map[string]any{"anchor_group_ids": []int64{}, "gemini_anchor": false, "metadata_group_ids": []int64{}, "rollout_percent": 0}
			if request.ExpectedRev == 2 {
				before["anchor_group_ids"] = []int64{31, 32}
				before["rollout_percent"] = 50
			}
			// Deliberately seed the pre-T2.2 shape. Defaults must be filled in
			// memory without a DB write or changing its revision.
			stored, err := json.Marshal(map[string]any{"rev": request.ExpectedRev, "spec": before})
			require.NoError(t, err)
			repo := &cacheRouteStore{rows: map[string]string{
				"cache_feature_master":            `{"rev":1,"spec":{"enabled":true}}`,
				"cache_observability_policy":      `{"rev":1,"spec":{"enabled":true}}`,
				"cache_anthropic_identity_policy": string(stored),
			}}
			groups := &cacheM4Groups{}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			options := []service.CachePolicyOption{
				service.WithCacheNorthstar(), service.WithCacheM4Policies(groups), service.WithCachePolicyAccounts(cacheM3OptionAccounts{}),
				service.WithCacheM4BuildCapabilities(capabilities), service.WithProviderCacheTrace(),
				service.WithCachePolicyClock(func() time.Time { return now }),
			}
			if setup.Fixture.Capabilities != nil {
				options = append(options, service.WithCacheAnthropicObservability())
			}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			require.Equal(t, string(stored), repo.rows["cache_anthropic_identity_policy"])
			require.Zero(t, groups.calls, "snapshot construction never queries the group table")
			audit := service.NewAuditLogService(&cacheRouteAudit{}, nil)
			router := cacheM4ContractRouter(policies, audit, "0.0.0-contract", service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			expected := cacheL03ExtendLegacyResponse(t, f.Response)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "full status/code/reason/metadata and success structure")
			if f.Request.Method != http.MethodPut {
				require.Equal(t, string(stored), repo.rows["cache_anthropic_identity_policy"], "validate must not persist defaults")
			}
			event, err := json.Marshal(map[string]any{"fixture": name, "status": status, "response": got})
			require.NoError(t, err)
			t.Logf("L04_POLICY_CONTRACT_EVENT %s", event)
		})
	}
}

func TestCachePolicyL04CompleteCombinedListReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/combo_t22"
	manifest, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	paths, err := filepath.Glob(filepath.Join(root, "combo_*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 7)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, hashes[filepath.Base(path)], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var setup struct {
				Fixture struct {
					Capabilities *service.CacheM4BuildCapabilities `json:"build_capabilities"`
				} `json:"_fixture"`
			}
			require.NoError(t, json.Unmarshal(raw, &setup))
			data, err := json.Marshal(f.Response["data"])
			require.NoError(t, err)
			var list service.CachePolicyList
			require.NoError(t, json.Unmarshal(data, &list))
			repo := &cacheRouteStore{rows: map[string]string{service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`}}
			for _, v := range list.Policies {
				seedPolicy(t, repo, v)
			}
			cfg := &config.Config{}
			cfg.Gateway.CacheFeatures.KillSwitch = list.Static.KillSwitch
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = list.Existing.Diagnostics
			cfg.Gateway.OpenAICache.AwareRoutingEnabled = list.Existing.AwareRouting
			cfg.Gateway.OpenAICache.RolloutPercent = list.Existing.Rollout
			cfg.Gateway.CacheTrace.Workers = list.Static.Caps.Workers
			cfg.Gateway.CacheTrace.MaxByteBudgetMiB = list.Static.Caps.MaxByteBudgetMiB
			cfg.Gateway.CacheTrace.MaxRateMiBPerS = list.Static.Caps.MaxRateMiBPerS
			cfg.Gateway.CacheTrace.MaxSessions = list.Static.Caps.MaxSessions
			cfg.Gateway.CacheTrace.ChainsPerSession = list.Static.Caps.ChainsPerSession
			cfg.Gateway.CacheTrace.MaxCheckpoints = list.Static.Caps.MaxCheckpoints
			groups := &cacheM4Groups{fail: strings.Contains(filepath.Base(path), "group_options_unavailable")}
			capabilities := service.CacheM4BuildCapabilities{L04: true, L03: true, L07: true}
			if setup.Fixture.Capabilities != nil {
				capabilities = *setup.Fixture.Capabilities
			}
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			options := []service.CachePolicyOption{
				service.WithCacheNorthstar(), service.WithCachePolicyAccounts(cacheM3OptionAccounts{}),
				service.WithCacheM4Policies(groups), service.WithCacheM4BuildCapabilities(capabilities),
				service.WithProviderCacheTrace(), service.WithCachePolicyClock(func() time.Time { return now }),
			}
			if filepath.Base(path) == "combo_m4l04_list_t22.json" {
				options = append(options, service.WithCacheAnthropicObservability())
			}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			require.Zero(t, groups.calls)
			audit := service.NewAuditLogService(&cacheRouteAudit{}, nil)
			router := cacheM4ContractRouter(policies, audit, list.BuildVersion, service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			expected := cacheL03ExtendLegacyResponse(t, f.Response)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "all cards including M2b, M31 and T22 fields; no projection")
			event, err := json.Marshal(map[string]any{"fixture": filepath.Base(path), "status": status, "response": got})
			require.NoError(t, err)
			t.Logf("L04_COMBINED_POLICY_CONTRACT_EVENT %s", event)
		})
	}
}

// Mechanical T2.2 extension for legacy M4 replay. Authoritative new complete
// responses are independently compared above; frozen inputs are never changed.
func cacheL04ExtendLegacyResponse(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	out := contractMap(t, input)
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["key"] == "anthropic_identity" && x["available"] == true {
				for _, field := range []string{"defaults", "stored", "loaded"} {
					value, _ := x[field].(map[string]any)
					if field != "defaults" {
						value, _ = value["spec"].(map[string]any)
					}
					if value != nil {
						value["metadata_rollout_percent"] = float64(0)
						value["max_inflight_per_anchor"] = float64(2)
					}
				}
				if schema, ok := x["schema"].([]any); ok {
					x["schema"] = append(schema, map[string]any{"field": "metadata_rollout_percent", "type": "int", "min": float64(0), "max": float64(100), "unit": "percent"}, map[string]any{"field": "max_inflight_per_anchor", "type": "int", "min": float64(1), "max": float64(16)})
				}
				return
			}
			for _, item := range x {
				walk(item)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(out)
	return cacheL03ExtendLegacyResponse(t, out)
}
