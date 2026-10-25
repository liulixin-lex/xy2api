//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// cachePOL2Store fails settings reads on demand; writes keep working, as on
// an instance whose refresh reads fail while administrator writes succeed.
type cachePOL2Store struct {
	*cacheRouteStore
	fail bool
}

func (r *cachePOL2Store) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if r.fail {
		return nil, errors.New("settings unavailable")
	}
	return r.cacheRouteStore.GetMultiple(ctx, keys)
}

// The PR C build (M3.1, T4.0, POL-2, M2b, T2.2, T2.3) replays the pol2
// samples that only describe upstream_identity, and F's combo_prc stale lists.
// Every other pol2 sample stays byte for byte and names its final successor:
// v1.0 lacks M3.1, and the pol2_m31 lists lack M2b observability and the
// T2.2/T2.3 anthropic_identity fields.
var cachePOL2Superseded = map[string]string{
	"pol2_m3_list_on.json":                     "prc_pol2_list_default_all_off.json",
	"pol2_m3_validate_enable.json":             "pol2_m31_m3_validate_enable.json",
	"pol2_m3_put_200.json":                     "pol2_m31_m3_put_200.json",
	"pol2_m4_list_on.json":                     "prc_pol2_list_on.json",
	"pol2_m4_list_masked_by_master.json":       "prc_pol2_list_masked_by_master.json",
	"pol2_m4_dormant_list_db_enabled.json":     "prc_pol2_list_dormant_db_enabled.json",
	"pol2_list_killed_and_stale.json":          "prc_pol2_list_kill_switch.json",
	"pol2_m31_m4_dormant_list_default.json":    "prc_pol2_list_default_all_off.json",
	"pol2_m31_m4_dormant_list_db_enabled.json": "prc_pol2_list_dormant_db_enabled.json",
	"pol2_m31_m4_list_on.json":                 "prc_pol2_list_on.json",
	"pol2_m31_m4_list_masked_by_master.json":   "prc_pol2_list_masked_by_master.json",
	"pol2_m31_list_killed_and_stale.json":      "prc_pol2_list_kill_switch.json",
}

func TestCachePolicyPOL2ContractReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/pol2"
	manifest, err := os.ReadFile(filepath.Join(root, "SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	dirs := map[string]string{}
	names := make([]string, 0, len(hashes))
	for name, digest := range hashes {
		raw, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(raw)), name)
		if _, superseded := cachePOL2Superseded[name]; !superseded {
			names = append(names, name)
			dirs[name] = root
		}
	}
	prcRoot, prcNames := cachePRCContractNames(t, "prc_pol2_")
	for _, name := range prcNames {
		names = append(names, name)
		dirs[name] = prcRoot
	}
	for old, successor := range cachePOL2Superseded {
		require.Contains(t, hashes, old)
		require.Contains(t, dirs, successor, "%s needs a replayed successor", old)
	}
	sort.Strings(names)
	require.Equal(t, []string{
		"pol2_m31_m3_put_200.json",
		"pol2_m31_m3_validate_enable.json",
		"prc_pol2_list_default_all_off.json",
		"prc_pol2_list_dormant_db_enabled.json",
		"prc_pol2_list_kill_switch.json",
		"prc_pol2_list_masked_by_master.json",
		"prc_pol2_list_on.json",
	}, names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			raw, err := os.ReadFile(filepath.Join(dirs[name], name))
			require.NoError(t, err)
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var meta struct {
				Fixture struct {
					SnapshotStale     bool                              `json:"snapshot_stale"`
					BuildCapabilities *service.CacheM4BuildCapabilities `json:"build_capabilities"`
				} `json:"_fixture"`
			}
			require.NoError(t, json.Unmarshal(raw, &meta))
			require.True(t, meta.Fixture.SnapshotStale)
			expected := contractMap(t, f.Response)
			data, _ := f.Response["data"].(map[string]any)
			repo := &cacheRouteStore{rows: map[string]string{
				service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`,
				"cache_feature_master":                      `{"rev":1,"spec":{"enabled":true}}`,
				"cache_observability_policy":                `{"rev":1,"spec":{"enabled":true}}`,
			}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			cfg.Gateway.CachePolicy.MaxStaleSeconds = 60
			version := "0.0.0-contract"
			putSetting := func(key string, rev int64, spec map[string]any) {
				b, err := json.Marshal(map[string]any{"rev": rev, "spec": spec})
				require.NoError(t, err)
				repo.rows[key] = string(b)
			}
			list := f.Request.Method == "GET" && f.Request.Path == "/api/v1/admin/cache/policies"
			validate := f.Request.Method == "POST" && f.Request.Path == "/api/v1/admin/cache/policies/upstream_identity/validate"
			put := f.Request.Method == "PUT" && f.Request.Path == "/api/v1/admin/cache/policies/upstream_identity"
			// Fixtures follow the M3/M4 replay of each source sample.
			switch {
			case list:
				b, err := json.Marshal(data)
				require.NoError(t, err)
				var view service.CachePolicyList
				require.NoError(t, json.Unmarshal(b, &view))
				version = view.BuildVersion
				cfg.Gateway.CacheFeatures.KillSwitch = view.Static.KillSwitch
				cfg.Gateway.OpenAICache.DiagnosticsEnabled = view.Existing.Diagnostics
				cfg.Gateway.OpenAICache.AwareRoutingEnabled = view.Existing.AwareRouting
				cfg.Gateway.OpenAICache.RolloutPercent = view.Existing.Rollout
				cfg.Gateway.CacheTrace.Workers = view.Static.Caps.Workers
				cfg.Gateway.CacheTrace.MaxByteBudgetMiB = view.Static.Caps.MaxByteBudgetMiB
				cfg.Gateway.CacheTrace.MaxRateMiBPerS = view.Static.Caps.MaxRateMiBPerS
				cfg.Gateway.CacheTrace.MaxSessions = view.Static.Caps.MaxSessions
				cfg.Gateway.CacheTrace.ChainsPerSession = view.Static.Caps.ChainsPerSession
				cfg.Gateway.CacheTrace.MaxCheckpoints = view.Static.Caps.MaxCheckpoints
				for _, policy := range view.Policies {
					if policy.Available {
						seedPolicy(t, repo, policy)
					}
				}
			case validate:
				rev, ok := data["current_rev"].(float64)
				require.True(t, ok)
				spec := map[string]any{"mode": "off", "account_ids": []int64{}, "rollout_percent": 0}
				diffs, ok := data["diff"].([]any)
				require.True(t, ok)
				for _, entry := range diffs {
					diff, ok := entry.(map[string]any)
					require.True(t, ok)
					field, ok := diff["field"].(string)
					require.True(t, ok)
					spec[field] = diff["before"]
				}
				warnings, ok := data["warnings"].([]any)
				require.True(t, ok)
				for _, warning := range warnings {
					if warning == "observability_off" {
						repo.rows["cache_observability_policy"] = `{"rev":0,"spec":{"enabled":false}}`
					}
				}
				putSetting("cache_upstream_identity_policy", int64(rev), spec)
			case put:
				var request struct {
					ExpectedRev int64 `json:"expected_rev"`
				}
				require.NoError(t, json.Unmarshal(f.Request.Body, &request))
				putSetting("cache_upstream_identity_policy", request.ExpectedRev, map[string]any{"mode": "off"})
			default:
				t.Fatalf("unexpected request %s %s", f.Request.Method, f.Request.Path)
			}
			store := &cachePOL2Store{cacheRouteStore: repo}
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			var options []service.CachePolicyOption
			if strings.HasPrefix(name, "prc_") {
				var stale bool
				options, stale = cachePRCBuildOptions(t, raw, clock)
				require.True(t, stale)
			} else {
				// upstream_identity only: the PR C production capabilities.
				require.Nil(t, meta.Fixture.BuildCapabilities)
				options = []service.CachePolicyOption{
					service.WithCacheNorthstar(),
					service.WithCachePolicyAccounts(cacheL03ContractAccounts{}),
					service.WithCacheM4Policies(&cacheM4Groups{}),
					service.WithProviderCacheTrace(),
					service.WithCacheAnthropicObservability(),
					service.WithCacheM4BuildCapabilities(service.CacheM4BuildCapabilities{L04: true, L03: true}),
					service.WithCachePolicyClock(clock),
				}
			}
			policies := service.NewCachePolicyService(store, cfg, options...)
			require.NoError(t, policies.Refresh(ctx))
			fresh := policies.Snapshot()
			// Refreshes fail beyond the bound; the request then reads settings again.
			store.fail = true
			now = now.Add(61 * time.Second)
			require.Error(t, policies.Refresh(ctx))
			require.NotSame(t, fresh, policies.Snapshot())
			require.False(t, policies.Snapshot().UpstreamIdentity().Enabled())
			store.fail = false

			audit := service.NewAuditLogService(&cacheRouteAudit{}, nil)
			router := cacheTestRouter(policies, audit, nil)
			if list {
				router = cacheM4ContractRouter(policies, audit, version, service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			}
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "compare every field")

			if put {
				// The committed enable stays off until the next successful refresh.
				require.False(t, policies.Snapshot().UpstreamIdentity().Enabled())
				require.NoError(t, policies.Refresh(ctx))
				require.True(t, policies.Snapshot().UpstreamIdentity().Enabled())
			}
		})
	}
}
