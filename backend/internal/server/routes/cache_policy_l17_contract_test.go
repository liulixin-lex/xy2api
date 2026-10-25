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
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// F's cache_policies_contract/l17 v1.1, copied byte for byte.
const cacheL17Root = "testdata/cache_policies_contract/l17"

func cacheL17Hashes(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cacheL17Root, "SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &hashes))
	require.Len(t, hashes, 36)
	for name, digest := range hashes {
		b, err := os.ReadFile(filepath.Join(cacheL17Root, name))
		require.NoError(t, err)
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(b)), name)
	}
	return hashes
}

// cacheL17ProjectLegacy applies the contract README §1 derivation to a frozen
// pre-L-17 response: upstream_identity gains the three grok fields at the end
// of its schema, the grok defaults, and the in-memory legacy fill of its
// stored spec. Candidates are this test fixture's Grok accounts unless the
// shared account_options_unavailable warning empties both pickers.
func cacheL17ProjectLegacy(t *testing.T, response map[string]any, l17 bool) map[string]any {
	t.Helper()
	out := contractMap(t, response)
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if schema, ok := v["schema"].([]any); ok && v["key"] == "upstream_identity" && v["available"] == true {
				options := []any{
					map[string]any{"id": float64(701), "name": "grok-oauth-a", "platform": "grok"},
					map[string]any{"id": float64(702), "name": "grok-key-b", "platform": "grok"},
				}
				effective := cacheM4As[map[string]any](t, v["effective"])
				for _, warning := range cacheM4As[[]any](t, effective["warnings"]) {
					if warning == "account_options_unavailable" {
						options = []any{}
					}
				}
				fields := []any{
					map[string]any{"field": "grok.chat_body_key", "type": "bool"},
					map[string]any{"field": "grok.compact_identity", "type": "bool"},
					map[string]any{"field": "grok.account_ids", "type": "id_list", "ref": "account", "options": options},
				}
				if !l17 {
					for _, field := range fields {
						field.(map[string]any)["not_implemented_in_build"] = true
					}
				}
				v["schema"] = append(schema, fields...)
				defaults := func() map[string]any {
					return map[string]any{"chat_body_key": false, "compact_identity": false, "account_ids": []any{}}
				}
				cacheM4As[map[string]any](t, v["defaults"])["grok"] = defaults()
				spec := cacheM4As[map[string]any](t, cacheM4As[map[string]any](t, v["stored"])["spec"])
				if _, ok := spec["grok"]; !ok {
					spec["grok"] = defaults()
				}
			}
			for _, item := range v {
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(out)
	return out
}

// The projection used for frozen pol2 and M4 negatives must reproduce every
// one of F's 17 authoritative derivatives exactly.
func TestCachePolicyL17LegacyProjectionMatchesAuthority(t *testing.T) {
	hashes := cacheL17Hashes(t)
	raw, err := os.ReadFile(filepath.Join(cacheL17Root, "SCOPE.json"))
	require.NoError(t, err)
	var scope struct {
		Derived   []string `json:"derived"`
		Unchanged []string `json:"unchanged_in_m31"`
	}
	require.NoError(t, json.Unmarshal(raw, &scope))
	require.Len(t, scope.Derived, 17)
	for _, name := range scope.Derived {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, hashes, "l17_"+name)
			raw, err := os.ReadFile(filepath.Join("testdata/cache_policies_contract/m31", name))
			require.NoError(t, err)
			var source cacheContract
			require.NoError(t, json.Unmarshal(raw, &source))
			raw, err = os.ReadFile(filepath.Join(cacheL17Root, "l17_"+name))
			require.NoError(t, err)
			var derived cacheContract
			require.NoError(t, json.Unmarshal(raw, &derived))
			require.Equal(t, contractMap(t, derived.Response), cacheL17ProjectLegacy(t, source.Response, false))
		})
	}
	// Samples without an upstream_identity card are unchanged by L-17.
	for _, name := range scope.Unchanged {
		raw, err := os.ReadFile(filepath.Join("testdata/cache_policies_contract/m31", name))
		require.NoError(t, err)
		var source cacheContract
		require.NoError(t, json.Unmarshal(raw, &source))
		require.Equal(t, contractMap(t, source.Response), cacheL17ProjectLegacy(t, source.Response, false), name)
	}
}

func cacheL17SetSpecField(spec map[string]any, field string, value any) {
	if child, ok := strings.CutPrefix(field, "grok."); ok {
		grok, _ := spec["grok"].(map[string]any)
		if grok == nil {
			grok = map[string]any{}
			spec["grok"] = grok
		}
		grok[child] = value
		return
	}
	spec[field] = value
}

// TestCachePolicyL17ContractReplay replays every sample native to l17 through
// the real admin HTTP handlers; the 17 derivatives replay inside the M3/M4
// suites. The build is M3-shaped (no M4 keys), with l17 from _fixture.
func TestCachePolicyL17ContractReplay(t *testing.T) {
	hashes := cacheL17Hashes(t)
	names := []string{}
	for name := range hashes {
		if strings.HasPrefix(name, "l17_") && !strings.HasPrefix(name, "l17_m31_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.Equal(t, []string{
		"l17_dormant_list_db_enabled.json",
		"l17_dormant_put_400_unsupported.json",
		"l17_dormant_validate_unsupported.json",
		"l17_list_grok_only_master_off.json",
		"l17_list_on.json",
		"l17_put_200.json",
		"l17_validate_account_rejected.json",
		"l17_validate_accounts_clear.json",
		"l17_validate_disable.json",
		"l17_validate_enable.json",
	}, names)
	observed := map[string]map[string]any{}
	requests := map[string]cacheContract{}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(cacheL17Root, name))
			require.NoError(t, err)
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			var meta struct {
				Fixture struct {
					BuildCapabilities map[string]bool `json:"build_capabilities"`
				} `json:"_fixture"`
			}
			require.NoError(t, json.Unmarshal(raw, &meta))
			l17, declared := meta.Fixture.BuildCapabilities["l17"]
			require.True(t, declared, "native l17 samples declare the capability")
			for _, key := range []string{"l04", "l03", "l07"} {
				require.False(t, meta.Fixture.BuildCapabilities[key], "M3-shaped build")
			}
			requests[name] = f
			expected := contractMap(t, f.Response)
			data, _ := f.Response["data"].(map[string]any)
			repo := &cacheRouteStore{rows: map[string]string{
				service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`,
				"cache_feature_master":                      `{"rev":1,"spec":{"enabled":true}}`,
				"cache_observability_policy":                `{"rev":1,"spec":{"enabled":true}}`,
			}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			version := "0.0.0-contract"
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			putSetting := func(rev int64, spec map[string]any) {
				b, err := json.Marshal(map[string]any{"rev": rev, "spec": spec})
				require.NoError(t, err)
				repo.rows["cache_upstream_identity_policy"] = string(b)
			}
			l01On := func() map[string]any {
				return map[string]any{"mode": "uuid_fill", "account_ids": []int64{101, 102}, "rollout_percent": 50}
			}
			list := f.Request.Method == http.MethodGet
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
			case strings.HasSuffix(f.Request.Path, "/validate"):
				// Before-values come from the diff; L-01 stays on (state_before on).
				rev := int64(cacheM4As[float64](t, data["current_rev"]))
				spec := l01On()
				for _, entry := range cacheM4As[[]any](t, data["diff"]) {
					diff := cacheM4As[map[string]any](t, entry)
					cacheL17SetSpecField(spec, cacheM4As[string](t, diff["field"]), diff["before"])
				}
				if name == "l17_validate_disable.json" {
					cacheL17SetSpecField(spec, "grok.account_ids", []int64{701})
				}
				putSetting(rev, spec)
			default:
				var request struct {
					ExpectedRev int64 `json:"expected_rev"`
				}
				require.NoError(t, json.Unmarshal(f.Request.Body, &request))
				putSetting(request.ExpectedRev, l01On())
			}
			options := []service.CachePolicyOption{service.WithCacheNorthstar(), service.WithCachePolicyAccounts(cacheM3OptionAccounts{}), service.WithCachePolicyClock(func() time.Time { return now })}
			if l17 {
				options = append(options, service.WithCacheL17())
			}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			audit := service.NewAuditLogService(&cacheRouteAudit{}, nil)
			router := cacheTestRouter(policies, audit, nil)
			if list {
				router = cacheM3ContractRouter(policies, audit, version)
			}
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "entire HTTP envelope and payload, including metadata.errors")
			event, err := json.Marshal(map[string]any{"fixture": filepath.Join(cacheL17Root, name), "status": status, "response": got})
			require.NoError(t, err)
			t.Logf("L17_POLICY_CONTRACT_EVENT %s", event)
			observed[name] = got
		})
	}
	// Each negative shares one canonical sample's request; the actual handler
	// result for that request must differ from the corrupted response.
	for name := range hashes {
		if !strings.HasPrefix(name, "invalid/") {
			continue
		}
		t.Run("reject/"+name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(cacheL17Root, name))
			require.NoError(t, err)
			var bad cacheContract
			require.NoError(t, json.Unmarshal(raw, &bad))
			matched := []string{}
			for source, canonical := range requests {
				if canonical.Request.Method == bad.Request.Method && canonical.Request.Path == bad.Request.Path && cacheL17SameJSON(t, canonical.Request.Body, bad.Request.Body) {
					matched = append(matched, source)
				}
			}
			require.NotEmpty(t, matched, "negative must share a canonical request")
			wrong := contractMap(t, bad.Response)
			normalizePolicyTimes(t, wrong)
			for _, source := range matched {
				actual, ok := observed[source]
				require.True(t, ok, "canonical HTTP must have passed")
				require.NotEqual(t, wrong, actual, "the corrupted contract response cannot pass full replay (%s)", source)
			}
		})
	}
}

func cacheL17SameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var x, y any
	require.NoError(t, json.Unmarshal(a, &x))
	require.NoError(t, json.Unmarshal(b, &y))
	return reflect.DeepEqual(x, y)
}
