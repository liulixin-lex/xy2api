//go:build unit

package service

import (
	"encoding/csv"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A's synthetic reference is shared as data, without importing repository code.
func TestCacheNorthstarOfflineReferenceAmounts(t *testing.T) {
	raw, err := os.ReadFile("../repository/testdata/northstar/rollup.csv")
	require.NoError(t, err)
	records, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	require.NoError(t, err)
	data := CacheNorthstarData{}
	for _, record := range records[1:] {
		m := map[string]string{}
		for i, key := range records[0] {
			m[key] = record[i]
		}
		number := func(key string) int64 { n, e := strconv.ParseInt(m[key], 10, 64); require.NoError(t, e); return n }
		at, e := time.Parse(time.RFC3339, m["bucket_start"])
		require.NoError(t, e)
		row := CacheNorthstarRow{BucketStart: at, Platform: m["platform"], Model: m["model_key"], GroupID: number("group_id"), ThresholdBand: int(number("threshold_band")), HasMedia: m["has_media"] == "t"}
		row.CacheNorthstarTotals = CacheNorthstarTotals{Requests: number("requests"), RequestsHit: number("requests_hit"), PromptTokens: number("p_tokens"), ReadTokens: number("read_tokens"), ReadMaybeRewritten: number("read_maybe_rewritten"), OpportunityTokens: number("o_recorded_tokens"), InputTokens: number("input_tokens_uncached"), CreationTokens: number("creation_tokens"), InputCostMicros: number("input_cost_micros"), ReadCostMicros: number("read_cost_micros"), CreationCostMicros: number("creation_cost_micros"), ReadChargedMicro: number("read_x_charged_mult_micro"), OpportunityChargedMicro: number("o_x_charged_mult_micro"), ReadUpstreamMicro: number("read_x_upstream_mult_micro"), OpportunityUpstreamMicro: number("o_x_upstream_mult_micro"), UpstreamCoveredPromptTokens: number("upstream_covered_p_tokens"), ComponentMismatchRequests: number("component_mismatch_requests")}
		data.Rows = append(data.Rows, row)
	}
	raw, err = os.ReadFile("../repository/testdata/northstar/api_northstar_expected.json")
	require.NoError(t, err)
	var expected struct {
		Data CacheNorthstarResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &expected))
	p := CacheNorthstarPolicy{PolicyVersion: "2026-10-22-v1", ExcludeMedia: true, ExcludedGroupIDs: []int64{99}, ModelThresholds: []CacheNorthstarThresholdRule{{Match: "exact", Model: "gpt-5.6", Threshold: 1024}, {Match: "prefix", Model: "claude-", Threshold: 2048}, {Match: "exact", Model: "gemini-2.5", Threshold: 4096}}}
	for at := expected.Data.Window.Start; at.Before(expected.Data.Window.End); at = at.Add(time.Hour) {
		data.Hours = append(data.Hours, CacheNorthstarHour{BucketStart: at})
	}
	actual := buildCacheNorthstar(expected.Data.CachePanelMeta, CachePanelQuery{Granularity: "1h", Platform: "all", CostView: "list"}, p, &CacheNorthstarState{PolicyChangedAt: expected.Data.Window.Start}, data, []CachePanelEvent{})
	require.Equal(t, expected.Data.Summary.CachePanelMetrics, actual.Summary.CachePanelMetrics)
	require.InDelta(t, *expected.Data.Summary.Cost.SavedAmount, *actual.Summary.Cost.SavedAmount, 1e-6)
	require.InDelta(t, *expected.Data.Summary.Cost.OpportunityAmount, *actual.Summary.Cost.OpportunityAmount, 1e-6)
	require.InDelta(t, *expected.Data.Summary.Cost.HitUpper, *actual.Summary.Cost.HitUpper, 1e-12)
	require.InDelta(t, *expected.Data.Summary.Cost.HitLower, *actual.Summary.Cost.HitLower, 1e-12)
	require.InDelta(t, expected.Data.Summary.Cost.PricedPromptShare, actual.Summary.Cost.PricedPromptShare, 1e-12)
	require.ElementsMatch(t, expected.Data.Summary.Cost.Notes, actual.Summary.Cost.Notes)
	require.Len(t, actual.Series, len(expected.Data.Series))
	for i, p := range expected.Data.Series {
		require.InDelta(t, *p.SavedAmount, *actual.Series[i].SavedAmount, 1e-6)
		require.InDelta(t, *p.OpportunityAmount, *actual.Series[i].OpportunityAmount, 1e-6)
		require.InDelta(t, *p.HitCostUpper, *actual.Series[i].HitCostUpper, 1e-12)
	}
}
func TestCacheNorthstarQueryValidation(t *testing.T) {
	for _, tc := range []struct{ kind, query string }{
		{"northstar", "window=1h"}, {"northstar", "granularity=5m"}, {"northstar", "platform=secret"}, {"northstar", "cost_view=bogus"}, {"northstar", "window=7d&window=24h"}, {"northstar", "user_id=1"},
		{"breakdown", ""}, {"breakdown", "dimension=session"}, {"breakdown", "dimension=group&limit=51"}, {"breakdown", "dimension=group&sort=cost"}, {"stats", "window=7d"}, {"stats", "cost_view=list"}, {"experiments", "window=24h"},
	} {
		t.Run(tc.kind+"?"+tc.query, func(t *testing.T) {
			q, e := url.ParseQuery(tc.query)
			require.NoError(t, e)
			_, e = ParseCachePanelQuery(tc.kind, q)
			require.Error(t, e)
		})
	}
	for _, kind := range []string{"northstar", "stats", "experiments"} {
		_, err := ParseCachePanelQuery(kind, url.Values{})
		require.NoError(t, err)
	}
	for _, dimension := range []string{"group", "account", "upstream_host", "client_family", "protocol", "model"} {
		_, err := ParseCachePanelQuery("breakdown", url.Values{"dimension": {dimension}, "cost_view": {"upstream"}, "sort": {"opportunity_amount"}})
		require.NoError(t, err)
	}
}
