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

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cacheM4Groups struct {
	fail   bool
	forbid bool
	calls  int
}

func (g *cacheM4Groups) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _ string, _ *bool) ([]service.Group, *pagination.PaginationResult, error) {
	g.calls++
	if g.forbid {
		panic("disable HTTP must not query groups")
	}
	if g.fail {
		return nil, nil, errors.New("GROUP_OPTIONS_HTTP_SECRET_ERROR_MARKER")
	}
	fallback := int64(77)
	return []service.Group{
		{ID: 18, Name: "openai-group", Platform: service.PlatformOpenAI},
		{ID: 31, Name: "claude-pool-a", Platform: service.PlatformAnthropic, AvailabilityFallbackGroupID: &fallback},
		{ID: 32, Name: "claude-pool-b", Platform: service.PlatformAnthropic},
		{ID: 77, Name: "claude-standby", Platform: service.PlatformAnthropic},
	}, &pagination.PaginationResult{Total: 4}, nil
}

func cacheM4ContractRouter(p *service.CachePolicyService, audit *service.AuditLogService, version string, actor service.CachePolicyActor) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: actor.UserID})
		c.Set(middleware.ContextKeyAuthEmail, actor.Email)
		c.Next()
	})
	group.Use(gin.HandlerFunc(middleware.NewAuditLogMiddleware(audit)))
	h := &handler.Handlers{Admin: &handler.AdminHandlers{CachePolicy: adminhandler.NewCachePolicyHandler(p, audit, version)}}
	registerCachePolicyRoutes(group, h)
	return router
}

func m4ContractSpec(t *testing.T, key string, enabled bool) map[string]any {
	t.Helper()
	name := "m4_list_default.json"
	if enabled {
		name = "m4_list_on.json"
	}
	raw, err := os.ReadFile(filepath.Join("testdata/cache_policies_contract/m3m4", name))
	require.NoError(t, err)
	var f cacheContract
	require.NoError(t, json.Unmarshal(raw, &f))
	for _, item := range f.Response["data"].(map[string]any)["policies"].([]any) {
		v := item.(map[string]any)
		if v["key"] == key {
			return contractMap(t, v["stored"].(map[string]any)["spec"])
		}
	}
	t.Fatalf("missing policy %s", key)
	return nil
}

func TestCachePolicyM4ContractReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/m3m4"
	b, err := os.ReadFile(filepath.Join(root, "M4_SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(b, &hashes))
	require.Len(t, hashes, 20)
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	observed := map[string]map[string]any{}
	requests := map[string]cacheContract{}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			requests[name] = f
			expected := contractMap(t, f.Response)
			data, _ := f.Response["data"].(map[string]any)
			repo := &cacheRouteStore{rows: map[string]string{
				"cache_feature_master":                      `{"rev":1,"spec":{"enabled":true}}`,
				"cache_observability_policy":                `{"rev":1,"spec":{"enabled":true}}`,
				service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`,
			}}
			auditRepo := &cacheRouteAudit{}
			audit := service.NewAuditLogService(auditRepo, nil)
			groups := &cacheM4Groups{fail: name == "m4_list_group_options_unavailable.json", forbid: name == "m4_put_routing_disable_skip_group_options.json" || strings.HasPrefix(name, "m4_validate_routing_disable")}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			version := "0.0.0-contract"
			key := "anthropic_identity"
			actor := service.CachePolicyActor{UserID: 1, Email: "admin@example.com"}
			if strings.Contains(f.Request.Path, "routing") {
				key = "routing"
				actor = service.CachePolicyActor{UserID: 2, Email: "oncall@example.com"}
			}
			setting := "cache_" + key + "_policy"
			put := func(rev int64, spec map[string]any) {
				b, err := json.Marshal(map[string]any{"rev": rev, "spec": spec})
				require.NoError(t, err)
				repo.rows[setting] = string(b)
			}
			switch {
			case strings.HasPrefix(name, "m4_list_"):
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
				for _, v := range list.Policies {
					seedPolicy(t, repo, v)
				}
			case strings.HasPrefix(name, "m4_validate_"):
				rev := int64(data["current_rev"].(float64))
				put(rev, m4ContractSpec(t, key, rev > 0))
			case name == "m4_put_routing_disable_skip_group_options.json":
				put(1, m4ContractSpec(t, key, true))
			case strings.HasPrefix(name, "m4_put_"):
				var request struct {
					ExpectedRev int64 `json:"expected_rev"`
				}
				require.NoError(t, json.Unmarshal(f.Request.Body, &request))
				put(request.ExpectedRev, m4ContractSpec(t, key, false))
			case strings.HasPrefix(name, "m4_history_"):
				var maxRev int64
				for _, v := range data["items"].([]any) {
					b, err := json.Marshal(v)
					require.NoError(t, err)
					var item service.CachePolicyHistoryItem
					require.NoError(t, json.Unmarshal(b, &item))
					require.NotNil(t, item.Actor)
					require.NotNil(t, item.RevBefore)
					require.NotNil(t, item.RevAfter)
					uid := item.Actor.UserID
					row := &service.AuditLog{CreatedAt: item.At, ActorUserID: &uid, ActorEmail: item.Actor.Email,
						Action: item.Action, Method: "PUT", StatusCode: item.StatusCode, Extra: map[string]any{
							"rev_before": *item.RevBefore, "rev_after": *item.RevAfter, "changed_fields": strings.Join(item.ChangedFields, ","),
						}}
					b, err = json.Marshal(map[string]any{"reason": item.Reason})
					require.NoError(t, err)
					row.RequestBody = string(b)
					auditRepo.rows = append(auditRepo.rows, row)
					maxRev = max(maxRev, *item.RevAfter)
				}
				put(maxRev, m4ContractSpec(t, key, false))
			default:
				t.Fatalf("unhandled fixture %s", name)
			}
			options := []service.CachePolicyOption{service.WithCacheNorthstar(), service.WithCachePolicyAccounts(cacheM3OptionAccounts{}), service.WithCacheM4Policies(groups), service.WithCachePolicyClock(func() time.Time { return now })}
			// The contract explicitly makes this one scenario a build with
			// Anthropic observation support; it is not a runtime toggle.
			if name == "m4_validate_anthropic_fallback_warning.json" {
				options = append(options, service.WithCacheAnthropicObservability())
			}
			policies := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, policies.Refresh(context.Background()))
			router := cacheM4ContractRouter(policies, audit, version, actor)
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			require.NotContains(t, fmt.Sprint(got), "GROUP_OPTIONS_HTTP_SECRET_ERROR_MARKER")
			if groups.forbid {
				require.Zero(t, groups.calls)
			}
			if groups.fail {
				require.Equal(t, 1, groups.calls)
			}
			if dir := os.Getenv("M4_POLICY_OUTPUT_DIR"); dir != "" {
				var record map[string]any
				require.NoError(t, json.Unmarshal(raw, &record))
				record["response"] = contractMap(t, got)
				record["status"] = status
				b, err := json.MarshalIndent(record, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(dir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0600))
			}
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "entire HTTP envelope and payload, including metadata.errors")
			observed[name] = got
		})
	}
	// Every negative shares its canonical scenario's request. Reuse the
	// actual handler result above: corrupted responses must not be accepted.
	b, err = os.ReadFile(filepath.Join(root, "M4_INVALID.json"))
	require.NoError(t, err)
	var invalid map[string]struct {
		Source string `json:"source"`
		SHA    string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal(b, &invalid))
	require.Len(t, invalid, 20)
	for name, entry := range invalid {
		t.Run("reject/"+name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, "invalid-m4", name))
			require.NoError(t, err)
			require.Equal(t, entry.SHA, fmt.Sprintf("%x", sha256.Sum256(raw)))
			var bad cacheContract
			require.NoError(t, json.Unmarshal(raw, &bad))
			canonical, ok := requests[entry.Source]
			require.True(t, ok)
			require.Equal(t, canonical.Request.Method, bad.Request.Method)
			require.Equal(t, canonical.Request.Path, bad.Request.Path)
			require.JSONEq(t, string(canonical.Request.Body), string(bad.Request.Body))
			actual, ok := observed[entry.Source]
			require.True(t, ok, "canonical HTTP must have passed")
			wrong := contractMap(t, bad.Response)
			normalizePolicyTimes(t, wrong)
			require.NotEqual(t, wrong, actual, "the corrupted contract response cannot pass full replay")
		})
	}
}

// Preserve the two M3 list edge scenarios that have no separate M4 sample.
// The expected new cards come from F's explicit M4 default fixture, never List.
func TestCachePolicyM4RetainsM3ListEdges(t *testing.T) {
	root := "testdata/cache_policies_contract/m3m4"
	raw, err := os.ReadFile(filepath.Join(root, "m4_list_default.json"))
	require.NoError(t, err)
	var defaults cacheContract
	require.NoError(t, json.Unmarshal(raw, &defaults))
	for _, name := range []string{"m3_list_account_options_unavailable.json", "m3_list_observability_off.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			var fixture cacheContract
			require.NoError(t, json.Unmarshal(raw, &fixture))
			expected := contractMap(t, fixture.Response)
			raw, err = json.Marshal(fixture.Response["data"])
			require.NoError(t, err)
			var list service.CachePolicyList
			require.NoError(t, json.Unmarshal(raw, &list))
			repo := &cacheRouteStore{rows: map[string]string{service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`}}
			for _, v := range list.Policies {
				if v.Available {
					seedPolicy(t, repo, v)
				}
			}
			newCards := defaults.Response["data"].(map[string]any)["policies"].([]any)
			cards := expected["data"].(map[string]any)["policies"].([]any)
			cards[4], cards[5] = contractMap(t, newCards[4]), contractMap(t, newCards[5])
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = list.Existing.Diagnostics
			cfg.Gateway.OpenAICache.AwareRoutingEnabled = list.Existing.AwareRouting
			cfg.Gateway.OpenAICache.RolloutPercent = list.Existing.Rollout
			cfg.Gateway.CacheFeatures.KillSwitch = list.Static.KillSwitch
			accounts := &cacheM3FaultAccounts{failOptions: name == "m3_list_account_options_unavailable.json"}
			groups := &cacheM4Groups{}
			policies := service.NewCachePolicyService(repo, cfg, service.WithCacheNorthstar(), service.WithCacheM4Policies(groups), service.WithCachePolicyAccounts(accounts))
			require.NoError(t, policies.Refresh(context.Background()))
			audit := service.NewAuditLogService(&cacheRouteAudit{}, nil)
			router := cacheM4ContractRouter(policies, audit, list.BuildVersion, service.CachePolicyActor{UserID: 1, Email: "admin@example.com"})
			status, got := cacheCall(t, router, fixture.Request.Method, fixture.Request.Path, fixture.Request.Body)
			require.Equal(t, fixture.Status, status)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got)
			require.Equal(t, 1, groups.calls)
		})
	}
}
