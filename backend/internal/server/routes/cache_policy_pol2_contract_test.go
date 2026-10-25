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

func TestCachePolicyPOL2ContractReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/pol2"
	manifest, err := os.ReadFile(filepath.Join(root, "SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	require.Len(t, hashes, 7)
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var meta struct {
				Fixture struct {
					SnapshotStale     bool                               `json:"snapshot_stale"`
					BuildCapabilities *service.CacheM4BuildCapabilities `json:"build_capabilities"`
				} `json:"_fixture"`
				DerivedFrom string `json:"_derived_from"`
			}
			require.NoError(t, json.Unmarshal(raw, &meta))
			require.True(t, meta.Fixture.SnapshotStale)
			expected := contractMap(t, f.Response)
			if meta.DerivedFrom != "m3_validate_enable.json" {
				expected = cachePOL2ProjectM31Upstream(t, expected)
			}
			data, _ := f.Response["data"].(map[string]any)
			m4Sample := meta.DerivedFrom == "m4_list_on.json" ||
				meta.DerivedFrom == "m4_list_masked_by_master.json" ||
				meta.DerivedFrom == "m4_dormant_list_db_enabled.json" ||
				name == "pol2_list_killed_and_stale.json"
			listSample := m4Sample || meta.DerivedFrom == "m3_list_on.json"
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
			if listSample {
				b, err := json.Marshal(data)
				require.NoError(t, err)
				var list service.CachePolicyList
				require.NoError(t, json.Unmarshal(b, &list))
				version = list.BuildVersion
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
				for _, policy := range list.Policies {
					if policy.Available {
						seedPolicy(t, repo, policy)
					}
				}
			} else {
				switch meta.DerivedFrom {
				case "m3_validate_enable.json":
					rev := int64(data["current_rev"].(float64))
					spec := map[string]any{"mode": "off", "account_ids": []int64{}, "rollout_percent": 0}
					for _, entry := range data["diff"].([]any) {
						diff, ok := entry.(map[string]any)
						require.True(t, ok)
						field, ok := diff["field"].(string)
						require.True(t, ok)
						spec[field] = diff["before"]
					}
					for _, warning := range data["warnings"].([]any) {
						if warning == "observability_off" {
							repo.rows["cache_observability_policy"] = `{"rev":0,"spec":{"enabled":false}}`
						}
					}
					putSetting("cache_upstream_identity_policy", rev, spec)
				case "m3_put_200.json":
					var request struct {
						ExpectedRev int64 `json:"expected_rev"`
					}
					require.NoError(t, json.Unmarshal(f.Request.Body, &request))
					putSetting("cache_upstream_identity_policy", request.ExpectedRev, map[string]any{"mode": "off"})
				default:
					t.Fatalf("unexpected source sample %q", meta.DerivedFrom)
				}
			}
			capabilities := service.CacheM4BuildCapabilities{}
			if m4Sample {
				capabilities = service.CacheM4BuildCapabilities{L04: true, L03: true, L07: true}
			}
			if meta.Fixture.BuildCapabilities != nil {
				capabilities = *meta.Fixture.BuildCapabilities
			}
			store := &cachePOL2Store{cacheRouteStore: repo}
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			groups := &cacheM4Groups{}
			policies := service.NewCachePolicyService(store, cfg,
				service.WithCacheNorthstar(),
				service.WithCachePolicyAccounts(&cacheM3FaultAccounts{}),
				service.WithCacheM4Policies(groups),
				service.WithCacheM4BuildCapabilities(capabilities),
				service.WithCachePolicyClock(func() time.Time { return now }),
			)
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
			if m4Sample {
				router = cacheM4ContractRouter(policies, audit, version, service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			} else if meta.DerivedFrom == "m3_list_on.json" {
				router = cacheM3ContractRouter(policies, service.NewAuditLogService(&cacheRouteAudit{}, nil), version)
			}
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "compare every field")

			if meta.DerivedFrom == "m3_put_200.json" {
				// The committed enable stays off until the next successful refresh.
				require.False(t, policies.Snapshot().UpstreamIdentity().Enabled())
				require.NoError(t, policies.Refresh(ctx))
				require.True(t, policies.Snapshot().UpstreamIdentity().Enabled())
			}
		})
	}
}

// M31 adds one upstream field to legacy POL2 responses; every other field
// remains byte-for-byte constrained by the published sample.
func cachePOL2ProjectM31Upstream(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	out := contractMap(t, response)
	data := cacheM4As[map[string]any](t, out["data"])
	var upstream map[string]any
	if policies, ok := data["policies"].([]any); ok {
		for _, item := range policies {
			policy := cacheM4As[map[string]any](t, item)
			if policy["key"] == "upstream_identity" {
				upstream = policy
				break
			}
		}
	} else if data["key"] == "upstream_identity" {
		upstream = data
	}
	if upstream == nil {
		return out
	}
	stored := cacheM4As[map[string]any](t, upstream["stored"])
	spec := cacheM4As[map[string]any](t, stored["spec"])
	require.NotContains(t, spec, "max_inflight_per_key")
	spec["max_inflight_per_key"] = float64(2)
	defaults := cacheM4As[map[string]any](t, upstream["defaults"])
	require.NotContains(t, defaults, "max_inflight_per_key")
	defaults["max_inflight_per_key"] = float64(2)
	schema := cacheM4As[[]any](t, upstream["schema"])
	for _, field := range schema {
		require.NotEqual(t, "max_inflight_per_key", cacheM4As[map[string]any](t, field)["field"])
	}
	upstream["schema"] = append(schema, map[string]any{"field": "max_inflight_per_key", "type": "int", "min": float64(1), "max": float64(16)})
	return out
}
