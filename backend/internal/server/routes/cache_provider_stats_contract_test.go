//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The counter inputs are fixed independently of expected responses. S1 reuses
// the same three partitions; S5 preserves OpenAI history after removing it.
func TestCacheProviderStatsHandlerContract(t *testing.T) {
	root := "testdata/cache_panel_m2b"
	raw, err := os.ReadFile(filepath.Join(root, "SOURCE_SHA256.json"))
	require.NoError(t, err)
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	for name, want := range manifest.Files {
		body, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, want, fmt.Sprintf("%x", sha256.Sum256(body)), name)
	}
	files, err := filepath.Glob(filepath.Join(root, "m2b_stats_*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			require.NoError(t, err)
			var sample map[string]any
			require.NoError(t, json.Unmarshal(raw, &sample))
			var fixture struct {
				Fixture struct {
					Platforms []string `json:"observability_platforms"`
					Set       string   `json:"counter_set"`
				} `json:"_fixture"`
				Request struct {
					Method, Path string
				} `json:"request"`
				Status int `json:"status"`
			}
			require.NoError(t, json.Unmarshal(raw, &fixture))
			store := &cacheRouteStore{rows: map[string]string{}}
			stored, err := json.Marshal(map[string]any{
				"rev": 7, "spec": map[string]any{"enabled": true, "platforms": fixture.Fixture.Platforms},
			})
			require.NoError(t, err)
			store.rows["cache_observability_policy"] = string(stored)
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			policies := service.NewCachePolicyService(store, cfg, service.WithCacheNorthstar(), service.WithProviderCacheTrace())
			require.NoError(t, policies.Refresh(context.Background()))
			counters := service.NewCacheTraceCounters()
			platforms := []string{"openai"}
			switch fixture.Fixture.Set {
			case "S1":
				platforms = []string{"openai", "anthropic", "gemini"}
			case "S2", "S5":
				platforms = []string{"openai", "anthropic"}
			}
			now := time.Now().UTC()
			for index, platform := range platforms {
				count := 12 + index*4
				for i := 0; i < count; i++ {
					at := now.Add(-time.Duration(count-i) * time.Millisecond)
					loss := []string{"under_read", "full", "cold"}[index]
					read := []int{1024, 2048, 0}[index]
					value := service.CacheTraceCounterSample{
						PromptTokens: 2048, ReadTokens: read, PreviousPrompt: 2048,
						LossClass: loss, Transport: "http", ModelGen: "gpt56", ForkKind: "append",
						PCKClassIn: "none", PCKClassOut: "injected", PCKOutIsUUID: true,
						SessHdrIn: 1, SessHdrOut: 1, SessOutRelation: "uuid_of_pck",
						RouteReason: "sticky_hit", RebindCause: "none",
					}
					if platform != "openai" {
						value.ModelGen = "non_openai"
						value.PCKClassIn, value.PCKClassOut, value.SessOutRelation = "", "", ""
						value.PCKOutIsUUID, value.SessHdrIn, value.SessHdrOut = false, 0, 0
					}
					if platform == "anthropic" {
						value.RouteReason, value.RebindCause = "session_rebind", "stall"
					}
					counters.RecordPlatformBusiness(at, platform, 2048, int64(read), platform+".example", "synthetic-session", "synthetic-model", int64(index+1))
					counters.RecordPlatformTrace(at, platform, value)
				}
			}
			panel := service.NewCacheObservabilityService(&cachePanelContractRepo{}, policies, counters, service.CachePanelBuildInfo{Version: "0.0.0-contract"})
			status, got := cacheCall(t, cachePanelRouter(panel, nil), fixture.Request.Method, fixture.Request.Path, nil)
			require.Equal(t, fixture.Status, status)
			require.EqualValues(t, 0, got["code"])
			// Schema/scope/sum/privacy checks are performed by F's validator on
			// these actual handler outputs, never on substituted expected data.
			sample["response"] = got
			if output := os.Getenv("CACHE_PROVIDER_STATS_CONTRACT_OUTPUT"); output != "" {
				require.NoError(t, os.MkdirAll(output, 0700))
				data, err := json.MarshalIndent(sample, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(output, filepath.Base(file)), data, 0600))
			}
			data := got["data"].(map[string]any)
			require.True(t, data["trace_enabled"].(bool))
			var frozen struct {
				Response struct {
					Data struct {
						Platforms []string `json:"platforms_covered"`
						Notes     []string `json:"scope_notes"`
					} `json:"data"`
				} `json:"response"`
			}
			require.NoError(t, json.Unmarshal(raw, &frozen))
			require.Equal(t, frozen.Response.Data.Platforms, cacheProviderStatsStrings(t, data["platforms_covered"]))
			require.Equal(t, frozen.Response.Data.Notes, cacheProviderStatsStrings(t, data["scope_notes"]))

			requestURL, err := url.Parse(fixture.Request.Path)
			require.NoError(t, err)
			selected := []string{requestURL.Query().Get("platform")}
			if selected[0] == "all" {
				selected = frozen.Response.Data.Platforms
			}
			var total, openAI int
			for index, platform := range platforms {
				for _, selectedPlatform := range selected {
					if platform == selectedPlatform {
						count := 12 + index*4
						total += count
						if platform == "openai" {
							openAI = count
						}
					}
				}
			}
			coverage := data["coverage"].(map[string]any)
			require.EqualValues(t, total, coverage["business_requests"])
			require.EqualValues(t, total, coverage["traced_requests"])
			require.EqualValues(t, total*2048, coverage["business_prompt_tokens"])
			require.EqualValues(t, total*2048, coverage["traced_prompt_tokens"])
			if total == 0 {
				require.Nil(t, coverage["coverage_ratio"])
			} else {
				require.EqualValues(t, 1, coverage["coverage_ratio"])
			}
			for _, field := range []string{"loss_classes", "route_reasons"} {
				require.EqualValues(t, total, cacheProviderStatsRequests(t, data[field]), field)
			}
			for _, field := range []string{"pck_in", "pck_out", "sess_out_relation"} {
				require.EqualValues(t, openAI, cacheProviderStatsRequests(t, data[field]), field)
			}
			if openAI == 0 {
				require.Nil(t, coverage["category_known_ratio"])
				require.Nil(t, data["pck_out_is_uuid_ratio"])
				require.Empty(t, data["session_headers"])
			} else {
				require.EqualValues(t, 1, coverage["category_known_ratio"])
				require.EqualValues(t, 1, data["pck_out_is_uuid_ratio"])
				require.NotEmpty(t, data["session_headers"])
			}
		})
	}
}

func cacheProviderStatsStrings(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	require.True(t, ok)
	out := make([]string, 0, len(items))
	for _, item := range items {
		str, ok := item.(string)
		require.True(t, ok)
		out = append(out, str)
	}
	return out
}

func cacheProviderStatsRequests(t *testing.T, value any) float64 {
	t.Helper()
	items, ok := value.([]any)
	require.True(t, ok)
	var total float64
	for _, item := range items {
		row, ok := item.(map[string]any)
		require.True(t, ok)
		total += row["requests"].(float64)
	}
	return total
}
