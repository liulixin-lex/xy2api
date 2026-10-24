//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cachePanelContractRepo struct {
	service.CacheNorthstarRepository
	state  *service.CacheNorthstarState
	data   service.CacheNorthstarData
	events []service.CachePanelEvent
}

func (r *cachePanelContractRepo) GetCacheNorthstarState(context.Context) (*service.CacheNorthstarState, error) {
	return r.state, nil
}
func (r *cachePanelContractRepo) ReadCacheNorthstar(_ context.Context, _ string, start, end time.Time, platform string) (service.CacheNorthstarData, error) {
	out := service.CacheNorthstarData{Rows: []service.CacheNorthstarRow{}, Hours: []service.CacheNorthstarHour{}}
	for _, row := range r.data.Rows {
		if !row.BucketStart.Before(start) && row.BucketStart.Before(end) && (platform == "all" || row.Platform == platform) {
			out.Rows = append(out.Rows, row)
		}
	}
	for _, hour := range r.data.Hours {
		if !hour.BucketStart.Before(start) && hour.BucketStart.Before(end) {
			out.Hours = append(out.Hours, hour)
		}
	}
	return out, nil
}
func (r *cachePanelContractRepo) ListCachePanelEvents(context.Context, time.Time, time.Time) ([]service.CachePanelEvent, error) {
	if r.events != nil {
		return r.events, nil
	}
	return []service.CachePanelEvent{}, nil
}

type cachePanelUnavailableCost struct {
	value service.CacheNorthstarResponse
}

func (r cachePanelUnavailableCost) Query(context.Context, string, service.CachePanelQuery) (any, error) {
	return r.value, nil
}

func cachePanelContractService(t *testing.T, ready, empty, trace bool, privateLabels ...bool) (*service.CacheObservabilityService, *service.CachePolicyService) {
	t.Helper()
	store := &cacheRouteStore{rows: map[string]string{}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	policies := service.NewCachePolicyService(store, cfg, service.WithCacheNorthstar())
	if trace {
		store.rows["cache_observability_policy"] = `{"rev":1,"spec":{"enabled":true},"updated_at":"2026-10-23T00:00:00Z"}`
		require.NoError(t, policies.Refresh(context.Background()))
		require.True(t, policies.Snapshot().TraceEnabled())
	}
	end := time.Now().UTC().Truncate(time.Hour)
	repo := &cachePanelContractRepo{}
	if ready {
		repo.state = &service.CacheNorthstarState{PolicyVersion: "2026-10-22-v1", AggregatedUntil: end, BackfilledFrom: end.Add(-60 * 24 * time.Hour), PolicyChangedAt: time.Unix(0, 0)}
		for at := repo.state.BackfilledFrom; at.Before(end); at = at.Add(time.Hour) {
			repo.data.Hours = append(repo.data.Hours, service.CacheNorthstarHour{BucketStart: at})
		}
		if !empty {
			row := service.CacheNorthstarRow{BucketStart: end.Add(-time.Hour), Platform: "openai", GroupID: 101, GroupLabel: "pool-a", AccountID: 201, AccountLabel: "relay-a-01", UpstreamHost: "relay-a.example", ClientFamily: "codex_cli_rs", Protocol: "responses_http", Model: "gpt-5.4", ThresholdBand: 2048,
				CacheNorthstarTotals: service.CacheNorthstarTotals{Requests: 12, RequestsHit: 9, PromptTokens: 48000, ReadTokens: 36000, InputTokens: 12000, InputCostMicros: 12000, ReadCostMicros: 3600, OpportunityTokens: 6000, ReadChargedMicro: 36000e6, OpportunityChargedMicro: 6000e6, ReadUpstreamMicro: 36000e6, OpportunityUpstreamMicro: 6000e6, UpstreamCoveredPromptTokens: 48000}}
			if len(privateLabels) > 0 && privateLabels[0] {
				row.AccountID, row.AccountLabel, row.GroupLabel = 102, "oauth.owner+tag@example.test", "group.owner@example.test"
			}
			repo.data.Rows = append(repo.data.Rows, row)
			row.GroupID, row.AccountID, row.Requests, row.RequestsHit = 102, 202, 2, 1
			row.GroupLabel, row.AccountLabel = "pool-b", "relay-b-01"
			if len(privateLabels) > 0 && privateLabels[0] {
				row.AccountID, row.AccountLabel, row.Requests = 101, "relay-a-01", 12
			}
			repo.data.Rows = append(repo.data.Rows, row)
		}
	}
	counters := service.NewCacheTraceCounters()
	if trace {
		for i := 0; i < 12; i++ {
			counters.RecordBusiness(time.Now(), 4000, 3000, "relay-a.example", "", "gpt-5.4", 201)
		}
	}
	return service.NewCacheObservabilityService(repo, policies, counters, service.CachePanelBuildInfo{Version: "0.0.0-contract"}), policies
}

func TestCacheNorthstarNeverEnabledReturnsNotReady(t *testing.T) {
	panel, policies := cachePanelContractService(t, false, false, false)
	require.False(t, policies.Snapshot().NorthstarRollupEnabled())
	require.Zero(t, policies.Snapshot().NorthstarRevision())
	router := cachePanelRouter(panel, nil)
	for _, path := range []string{"northstar", "breakdown?dimension=group"} {
		status, body := cacheCall(t, router, "GET", "/api/v1/admin/cache/"+path, nil)
		require.Equal(t, 503, status)
		require.EqualValues(t, 503, body["code"])
		require.Equal(t, "CACHE_ROLLUP_NOT_READY", body["reason"])
		require.NotContains(t, body, "data")
	}
}

func TestCacheNorthstarHandlerContract(t *testing.T) {
	root := filepath.Join("testdata", "cache_panel")
	sums, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.Fields(line)
		require.Len(t, parts, 2)
		raw, e := os.ReadFile(filepath.Join(root, parts[1]))
		require.NoError(t, e)
		require.Equal(t, parts[0], fmt.Sprintf("%x", sha256.Sum256(raw)), parts[1])
	}
	files, err := filepath.Glob(filepath.Join(root, "*.json"))
	require.NoError(t, err)
	exports := os.Getenv("CACHE_PANEL_CONTRACT_OUTPUT")
	if exports != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(exports, "invalid"), 0o755))
		schema, e := os.ReadFile(filepath.Join(root, "cache_panel.v1.schema.json"))
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(filepath.Join(exports, "cache_panel.v1.schema.json"), schema, 0o644))
	}
	for _, file := range files {
		name := filepath.Base(file)
		if strings.HasSuffix(name, ".schema.json") || name == "experiments_m3_preview.json" {
			continue
		} // Explicitly outside M2.
		t.Run(name, func(t *testing.T) {
			raw, e := os.ReadFile(file)
			require.NoError(t, e)
			var sample struct {
				Contract []string `json:"_contract"`
				Request  struct {
					Method, Path string
					Body         any
				} `json:"request"`
				Status   int            `json:"status"`
				Response map[string]any `json:"response"`
			}
			require.NoError(t, json.Unmarshal(raw, &sample))
			panel, _ := cachePanelContractService(t, sample.Status != 503, name == "northstar_no_data.json", name == "stats_24h.json", name == "breakdown_account_masked_label.json")
			var reader adminhandler.CachePanelReader = panel
			// Cost-column availability is a DTO compatibility shape; it is not a
			// claim that the current migration can omit settled cost columns.
			if name == "northstar_cost_unavailable.json" {
				data, e := json.Marshal(sample.Response["data"])
				require.NoError(t, e)
				var value service.CacheNorthstarResponse
				require.NoError(t, json.Unmarshal(data, &value))
				reader = cachePanelUnavailableCost{value: value}
			}
			var auth gin.HandlerFunc
			if sample.Status == 403 {
				auth = cacheOrdinaryUserAuth(t)
			}
			router := cachePanelRouter(reader, auth)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(sample.Request.Method, sample.Request.Path, nil))
			require.Equal(t, sample.Status, rec.Code, rec.Body.String())
			var response map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, sample.Response["code"], response["code"])
			if sample.Status != 200 {
				require.Equal(t, sample.Response["reason"], response["reason"])
				require.NotContains(t, response, "data")
			} else {
				require.Equal(t, "success", response["message"])
				data := response["data"].(map[string]any)
				require.Equal(t, data["as_of"], data["window"].(map[string]any)["end"])
				require.NotEmpty(t, data["policy_version"])
				if strings.Contains(sample.Request.Path, "/northstar") || strings.Contains(sample.Request.Path, "/breakdown") {
					require.Equal(t, "private, max-age=60", rec.Header().Get("Cache-Control"))
				} else {
					require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
				}
				if name == "stats_trace_disabled.json" {
					require.Equal(t, false, data["trace_enabled"])
					require.Empty(t, data["loss_classes"])
				}
				if name == "stats_24h.json" {
					require.Equal(t, true, data["trace_enabled"])
				}
				if name == "experiments_empty_m2.json" {
					require.Empty(t, data["experiments"])
				}
			}
			if exports != "" {
				actual := map[string]any{"_contract": sample.Contract, "_note": "Actual HTTP handler output on synthetic repository rows; cost_unavailable uses its typed DTO shape.", "request": map[string]any{"method": sample.Request.Method, "path": sample.Request.Path, "body": nil}, "status": rec.Code, "response": response}
				actualRaw, e := json.MarshalIndent(actual, "", "  ")
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(filepath.Join(exports, name), actualRaw, 0o644))
			}
		})
	}
}

func TestCacheNorthstarHandlerNonUTCHourAndEvents(t *testing.T) {
	_, policies := cachePanelContractService(t, false, false, false)
	at := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour).In(time.FixedZone("Asia/Shanghai", 8*3600))
	cold := at.Add(time.Hour)
	repo := &cachePanelContractRepo{
		state: &service.CacheNorthstarState{PolicyVersion: "2026-10-22-v1", AggregatedUntil: cold, BackfilledFrom: at, PolicyChangedAt: at},
		data: service.CacheNorthstarData{Rows: []service.CacheNorthstarRow{{BucketStart: at, Platform: "openai", Model: "gpt-5.4", ThresholdBand: 2048,
			CacheNorthstarTotals: service.CacheNorthstarTotals{Requests: 12, RequestsHit: 6, PromptTokens: 24000, ReadTokens: 12000, InputTokens: 12000, InputCostMicros: 24000, ReadCostMicros: 1200}}}, Hours: []service.CacheNorthstarHour{{BucketStart: at}}},
		events: []service.CachePanelEvent{{At: at, Kind: "release", Label: "test", Version: "0.0.0-contract", ColdUntil: &cold}},
	}
	panel := service.NewCacheObservabilityService(repo, policies, nil, service.CachePanelBuildInfo{})
	router := cachePanelRouter(panel, nil)
	path := "/api/v1/admin/cache/northstar?window=24h"
	status, body := cacheCall(t, router, "GET", path, nil)
	require.Equal(t, 200, status)
	data := body["data"].(map[string]any)
	require.Equal(t, data["as_of"], data["window"].(map[string]any)["end"])
	total := float64(0)
	for _, value := range data["series"].([]any) {
		point := value.(map[string]any)
		total += point["requests"].(float64)
		if point["requests"].(float64) > 0 {
			require.Equal(t, 0.5, point["token_hit_upper"])
			require.NotNil(t, point["saved_amount"])
			require.False(t, point["partial"].(bool))
		}
	}
	require.Equal(t, 12.0, total)
	event := data["events"].([]any)[0].(map[string]any)
	require.Equal(t, at.UTC().Format(time.RFC3339), event["at"])
	require.Equal(t, cold.UTC().Format(time.RFC3339), event["cold_until"])
	if output := os.Getenv("CACHE_PANEL_CONTRACT_OUTPUT"); output != "" {
		require.NoError(t, os.MkdirAll(output, 0o755))
		raw, err := json.MarshalIndent(map[string]any{"_contract": []string{"P-1", "P-4", "P-5"}, "_note": "Actual handler with non-UTC repository rows and events.", "request": map[string]any{"method": "GET", "path": path, "body": nil}, "status": status, "response": body}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(output, "northstar_timezone.json"), raw, 0o644))
	}
}

func TestCacheNorthstarHandlerMasksPrivateLabels(t *testing.T) {
	for _, dimension := range []string{"account", "group"} {
		for _, window := range []string{"24h", "7d", "30d"} {
			t.Run(dimension+"_"+window, func(t *testing.T) {
				panel, _ := cachePanelContractService(t, true, false, false, true)
				path := "/api/v1/admin/cache/breakdown?dimension=" + dimension + "&window=" + window
				status, body := cacheCall(t, cachePanelRouter(panel, nil), "GET", path, nil)
				require.Equal(t, 200, status)
				data := body["data"].(map[string]any)
				labels := map[string]string{}
				for _, item := range data["rows"].([]any) {
					row := item.(map[string]any)
					labels[row["key"].(string)] = row["label"].(string)
					if dimension == "account" && row["key"] == "102" {
						require.Equal(t, row["meta"].(map[string]any)["platform"].(string)+"#"+row["key"].(string), row["label"])
					}
				}
				if dimension == "account" {
					require.Equal(t, map[string]string{"102": "openai#102", "101": "relay-a-01"}, labels)
				} else {
					require.Equal(t, map[string]string{"101": "group#101", "102": "pool-b"}, labels)
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "@")
				if output := os.Getenv("CACHE_PANEL_CONTRACT_OUTPUT"); output != "" {
					require.NoError(t, os.MkdirAll(output, 0o755))
					raw, err = json.MarshalIndent(map[string]any{
						"_contract": []string{"P-3", "P-6"}, "_note": "Actual HTTP response with synthetic private labels.",
						"request": map[string]any{"method": "GET", "path": path, "body": nil}, "status": status, "response": body,
					}, "", "  ")
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(output, "breakdown_private_"+dimension+"_"+window+".json"), raw, 0o644))
				}
			})
		}
	}
}
