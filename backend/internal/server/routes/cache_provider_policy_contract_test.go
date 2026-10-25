//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// These append-only build fixtures are compared as complete HTTP envelopes.
// M1 fixtures remain frozen; no expected availability or spec is rewritten.
func TestCacheProviderPolicyHTTPContracts(t *testing.T) {
	root := "testdata/cache_policies_m2b"
	raw, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	names := make([]string, 0)
	for name, want := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, want, fmt.Sprintf("%x", sha256.Sum256(raw)), name)
		if !strings.Contains(name, "/") && (strings.HasPrefix(name, "m2_") || strings.HasPrefix(name, "m2b_")) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.Len(t, names, 23)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var seed struct {
				Fixture *struct {
					Rev  int64          `json:"stored_raw_rev"`
					Spec map[string]any `json:"stored_raw_spec"`
				} `json:"_fixture"`
			}
			require.NoError(t, json.Unmarshal(raw, &seed))
			repo := &cacheRouteStore{rows: map[string]string{
				service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`,
			}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			options := []service.CachePolicyOption{service.WithCacheNorthstar()}
			if strings.HasPrefix(name, "m2b_") {
				options = append(options, service.WithProviderCacheTrace())
			}
			key := "cache_observability_policy"
			if f.Request.Method != "GET" {
				var write service.CachePolicyWrite
				require.NoError(t, json.Unmarshal(f.Request.Body, &write))
				b, err := json.Marshal(map[string]any{"rev": write.ExpectedRev, "spec": map[string]any{}})
				require.NoError(t, err)
				repo.rows[key] = string(b)
			}
			var afterRefresh func()
			if strings.Contains(name, "_list_") {
				data, err := json.Marshal(f.Response["data"])
				require.NoError(t, err)
				var list service.CachePolicyList
				require.NoError(t, json.Unmarshal(data, &list))
				cfg.Gateway.CacheFeatures.KillSwitch = list.Static.KillSwitch
				cfg.Gateway.CacheTrace.MaxByteBudgetMiB = list.Static.Caps.MaxByteBudgetMiB
				for _, view := range list.Policies {
					if view.Available {
						seedPolicy(t, repo, view)
					}
				}
				if strings.HasSuffix(name, "list_pending_reload.json") {
					saved := repo.rows[key]
					repo.rows[key] = `{"rev":4,"spec":{"enabled":false}}`
					afterRefresh = func() { repo.rows[key] = saved }
				}
				if strings.HasSuffix(name, "list_parse_error.json") {
					var stored map[string]any
					require.NoError(t, json.Unmarshal([]byte(repo.rows[key]), &stored))
					stored["spec"] = map[string]any{"enabled": true, "future_option": true}
					b, err := json.Marshal(stored)
					require.NoError(t, err)
					repo.rows[key] = string(b)
				}
			} else if seed.Fixture != nil {
				b, err := json.Marshal(map[string]any{"rev": seed.Fixture.Rev, "spec": seed.Fixture.Spec})
				require.NoError(t, err)
				repo.rows[key] = string(b)
			} else if f.Request.Method == "PUT" && f.Status == 200 {
				data, err := json.Marshal(f.Response["data"])
				require.NoError(t, err)
				var view service.CachePolicyView
				require.NoError(t, json.Unmarshal(data, &view))
				view.Stored.Rev--
				switch {
				case strings.HasSuffix(name, "put_200_platforms.json"):
					view.Stored.Spec["platforms"] = []string{"openai"}
				case strings.HasSuffix(name, "put_partial_update.json"):
					view.Stored.Spec["rate_mib_per_s"] = 32
				default:
					view.Stored.Spec["enabled"] = false
				}
				seedPolicy(t, repo, view)
			}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			if afterRefresh != nil {
				afterRefresh()
			}
			before := repo.rows[key]
			auditRepo := &cacheRouteAudit{}
			audit := service.NewAuditLogService(auditRepo, nil)
			status, got := cacheCall(t, cacheTestRouter(policies, audit, nil), f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status, got)
			if output := os.Getenv("CACHE_PROVIDER_POLICY_CONTRACT_OUTPUT"); output != "" {
				require.NoError(t, os.MkdirAll(output, 0700))
				b, err := json.MarshalIndent(map[string]any{"status": status, "response": got}, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(output, name), b, 0600))
			}
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, f.Response)
			require.Equal(t, f.Response, got)
			audit.Start()
			audit.Stop()
			if strings.HasSuffix(f.Request.Path, "/validate") {
				require.Empty(t, auditRepo.rows)
				require.Equal(t, before, repo.rows[key])
			}
			if seed.Fixture != nil && f.Request.Method == "PUT" {
				require.Len(t, auditRepo.rows, 1)
				require.Equal(t, "rate_mib_per_s", auditRepo.rows[0].Extra["changed_fields"])
				require.Equal(t, int64(3), auditRepo.rows[0].Extra["rev_before"])
				require.Equal(t, int64(4), auditRepo.rows[0].Extra["rev_after"])
			}
		})
	}
}
