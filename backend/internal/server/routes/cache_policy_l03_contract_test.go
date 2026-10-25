//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cacheL03ContractAccounts struct {
	cacheM3OptionAccounts
	fail bool
}

func cacheL03AccountRows() []service.Account {
	return []service.Account{
		{ID: 501, Name: "relay-claude-a", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}},
		{ID: 502, Name: "relay-claude-b", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay2.invalid"}},
		{ID: 503, Name: "official", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey},
		{ID: 504, Name: "oauth", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth},
	}
}
func (r cacheL03ContractAccounts) GetByIDs(ctx context.Context, ids []int64) ([]*service.Account, error) {
	out, err := r.cacheM3OptionAccounts.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range cacheL03AccountRows() {
		for _, id := range ids {
			if id == row.ID {
				a := row
				out = append(out, &a)
			}
		}
	}
	return out, nil
}
func (r cacheL03ContractAccounts) ListWithFilters(ctx context.Context, p pagination.PaginationParams, platform, kind, status, search string, gid int64, privacy string) ([]service.Account, *pagination.PaginationResult, error) {
	if platform == service.PlatformAnthropic {
		if r.fail {
			return nil, nil, fmt.Errorf("PRIVATE_OPTIONS_FAILURE")
		}
		return cacheL03AccountRows(), &pagination.PaginationResult{Total: 4}, nil
	}
	return r.cacheM3OptionAccounts.ListWithFilters(ctx, p, platform, kind, status, search, gid, privacy)
}

func TestCachePolicyL03SinglePolicyContractReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/m4l03"
	raw, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &hashes))
	for _, name := range []string{"m4l03_validate_metadata_enable.json", "m4l03_validate_account_rejected.json", "m4l03_validate_accounts_clear.json", "m4l03_put_metadata_enable_200.json", "m4l03_put_400_official_endpoint.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var req struct {
				ExpectedRev int64 `json:"expected_rev"`
			}
			require.NoError(t, json.Unmarshal(f.Request.Body, &req))
			spec := map[string]any{"anchor_group_ids": []int64{31, 32}, "gemini_anchor": false, "metadata_group_ids": []int64{}, "metadata_account_ids": []int64{}, "rollout_percent": 50, "metadata_rollout_percent": 0, "max_inflight_per_anchor": 2}
			if req.ExpectedRev == 3 {
				spec["metadata_group_ids"] = []int64{31}
				spec["metadata_account_ids"] = []int64{501}
				spec["metadata_rollout_percent"] = 20
			}
			stored, err := json.Marshal(map[string]any{"rev": req.ExpectedRev, "spec": spec})
			require.NoError(t, err)
			repo := &cacheRouteStore{rows: map[string]string{"cache_feature_master": `{"rev":1,"spec":{"enabled":true}}`, "cache_anthropic_identity_policy": string(stored)}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			options := []service.CachePolicyOption{service.WithCacheNorthstar(), service.WithCacheM4Policies(&cacheM4Groups{}), service.WithCachePolicyAccounts(cacheL03ContractAccounts{}), service.WithProviderCacheTrace(), service.WithCacheAnthropicObservability(), service.WithCacheM4BuildCapabilities(service.CacheM4BuildCapabilities{L04: true, L03: true}), service.WithCachePolicyClock(func() time.Time { return time.Date(2026, 10, 25, 5, 10, 0, 0, time.UTC) })}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			router := cacheM4ContractRouter(policies, service.NewAuditLogService(&cacheRouteAudit{}, nil), "0.0.0-contract", service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			expected := contractMap(t, f.Response)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got)
			event, err := json.Marshal(map[string]any{"fixture": name, "status": status, "response": got})
			require.NoError(t, err)
			t.Logf("L03_POLICY_CONTRACT_EVENT %s", event)
		})
	}
}

// The authoritative L03 list fixtures predate M2b/M31 fields on other cards.
// Their independent, mechanical extensions are applied without projecting out
// any part of the actual HTTP response. Frozen sample bytes remain unchanged.
func TestCachePolicyL03CompleteCombinedListReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/m4l03"
	raw, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &hashes))
	paths, err := filepath.Glob(filepath.Join(root, "m4l03_*.json"))
	require.NoError(t, err)
	count := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var f cacheContract
		require.NoError(t, json.Unmarshal(raw, &f))
		if f.Request.Method != "GET" {
			continue
		}
		name := filepath.Base(path)
		count++
		t.Run(name, func(t *testing.T) {
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
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
			caps := service.CacheM4BuildCapabilities{L04: true, L03: true, L07: true}
			if setup.Fixture.Capabilities != nil {
				caps = *setup.Fixture.Capabilities
			}
			options := []service.CachePolicyOption{service.WithCacheNorthstar(), service.WithCacheM4Policies(&cacheM4Groups{fail: strings.Contains(name, "group_options_unavailable")}), service.WithCachePolicyAccounts(cacheL03ContractAccounts{fail: strings.Contains(name, "account_options_unavailable")}), service.WithProviderCacheTrace(), service.WithCacheM4BuildCapabilities(caps), service.WithCachePolicyClock(func() time.Time { return time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC) })}
			if !strings.Contains(name, "_m4_") {
				options = append(options, service.WithCacheAnthropicObservability())
			}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			router := cacheM4ContractRouter(policies, service.NewAuditLogService(&cacheRouteAudit{}, nil), list.BuildVersion, service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			expected := cacheL03ComposeOtherCards(t, f.Response)
			// PR C samples predate T2.5: an l07 build adds the m4l07 routing leaves
			// (F derivation, proven by TestCachePolicyM4L07OverlayMatchesAuthority).
			expected = cacheM4L07Overlay(t, expected)
			// P3 builds add the T3.1a routing leaf and the T3.4 grok leaves; neither is
			// declared here (F derivations, proven by TestCachePolicyL09AOverlayMatchesAuthority
			// and TestCachePolicyL17LegacyProjectionMatchesAuthority).
			expected = cacheL17ProjectLegacy(t, cacheL09AOverlay(t, expected, false), false)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got)
			event, err := json.Marshal(map[string]any{"fixture": name, "status": status, "response": got})
			require.NoError(t, err)
			t.Logf("L03_COMBINED_POLICY_CONTRACT_EVENT %s", event)
		})
	}
	require.Equal(t, 9, count)
}

func cacheL03ComposeOtherCards(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	out := contractMap(t, input)
	policies := out["data"].(map[string]any)["policies"].([]any)
	for _, item := range policies {
		card := item.(map[string]any)
		key := card["key"]
		if key != "observability" && key != "upstream_identity" {
			continue
		}
		defaults := card["defaults"].(map[string]any)
		stored := card["stored"].(map[string]any)["spec"].(map[string]any)
		schema := card["schema"].([]any)
		if key == "upstream_identity" {
			defaults["max_inflight_per_key"] = float64(2)
			stored["max_inflight_per_key"] = float64(2)
			schema = append(schema, map[string]any{"field": "max_inflight_per_key", "type": "int", "min": float64(1), "max": float64(16)})
			if stored["mode"] == "uuid_fill" && len(stored["account_ids"].([]any)) == 0 {
				effective := card["effective"].(map[string]any)
				effective["warnings"] = append(effective["warnings"].([]any), "l01_all_accounts_unverified")
			}
		} else {
			defaults["platforms"] = []any{"openai"}
			defaults["ingress_chain_percent"] = float64(10)
			stored["platforms"] = []any{"openai"}
			stored["ingress_chain_percent"] = min(float64(10), stored["chain_percent"].(float64))
			insert := len(schema) - 1
			additions := []any{map[string]any{"field": "platforms", "type": "id_list", "options": []any{"openai", "anthropic", "gemini"}}, map[string]any{"field": "ingress_chain_percent", "type": "int", "min": float64(0), "max": float64(100), "unit": "percent"}}
			// Match the frozen M2b enum list, including the Messages bridge.
			additions[0] = map[string]any{"field": "platforms", "type": "enum_list", "options": []any{"openai", "anthropic", "gemini", "messages_bridge"}}
			next := append([]any{}, schema[:insert]...)
			next = append(next, additions...)
			schema = append(next, schema[insert:]...)
		}
		card["schema"] = schema
	}
	return out
}

// Extend earlier fixtures mechanically without modifying any frozen input.
// Metadata capability follows the adjacent metadata group field in that build.
func cacheL03ExtendLegacyResponse(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	out := contractMap(t, input)
	var walk func(any)
	walk = func(value any) {
		switch x := value.(type) {
		case map[string]any:
			if x["key"] == "anthropic_identity" && x["available"] == true {
				for _, name := range []string{"defaults", "stored", "loaded"} {
					node, _ := x[name].(map[string]any)
					if name != "defaults" {
						node, _ = node["spec"].(map[string]any)
					}
					if node != nil {
						if _, found := node["metadata_account_ids"]; !found {
							node["metadata_account_ids"] = []any{}
						}
					}
				}
				if schema, ok := x["schema"].([]any); ok {
					for _, item := range schema {
						if item.(map[string]any)["field"] == "metadata_account_ids" {
							return
						}
					}
					next := []any{}
					for _, item := range schema {
						next = append(next, item)
						field := item.(map[string]any)
						if field["field"] == "metadata_group_ids" {
							account := map[string]any{"field": "metadata_account_ids", "type": "id_list", "ref": "account", "options": []any{}}
							if field["not_implemented_in_build"] == true {
								account["not_implemented_in_build"] = true
							}
							next = append(next, account)
						}
					}
					x["schema"] = next
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
	return out
}
