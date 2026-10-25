//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Synthetic settled rows use the same model-level weighted prices and request
// multipliers as A's R-5; the expected amounts are calculated independently.
func TestCacheNorthstarSettledCostViews(t *testing.T) {
	hour := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	rows := []CacheNorthstarRow{
		{BucketStart: hour, Platform: PlatformOpenAI, Model: "gpt-5.4", CacheNorthstarTotals: CacheNorthstarTotals{
			Requests: 10, RequestsHit: 10, PromptTokens: 3000, ReadTokens: 2000, InputTokens: 1000, OpportunityTokens: 500,
			InputCostMicros: 10000, ReadCostMicros: 2000, ReadChargedMicro: 4000e6, OpportunityChargedMicro: 1000e6,
			ReadUpstreamMicro: 1000e6, OpportunityUpstreamMicro: 250e6, UpstreamCoveredPromptTokens: 3000,
		}},
		{BucketStart: hour.Add(time.Hour), Platform: PlatformOpenAI, Model: "gpt-5.4", CacheNorthstarTotals: CacheNorthstarTotals{
			Requests: 10, RequestsHit: 10, PromptTokens: 3000, ReadTokens: 2000, InputTokens: 1000, OpportunityTokens: 500,
			InputCostMicros: 20000, ReadCostMicros: 2000, ReadChargedMicro: 1000e6, OpportunityChargedMicro: 250e6,
		}},
		{BucketStart: hour, Platform: PlatformAnthropic, Model: "claude-test", CacheNorthstarTotals: CacheNorthstarTotals{
			Requests: 10, RequestsHit: 10, PromptTokens: 3000, ReadTokens: 2000, CreationTokens: 1000, OpportunityTokens: 500,
			CreationCostMicros: 30000, ReadCostMicros: 3000, ReadMaybeRewritten: 2000,
			ReadChargedMicro: 3000e6, OpportunityChargedMicro: 750e6,
			ReadUpstreamMicro: 2000e6, OpportunityUpstreamMicro: 500e6, UpstreamCoveredPromptTokens: 3000,
			ComponentMismatchRequests: 1,
		}},
		{BucketStart: hour, Platform: "grok", Model: "unpriced", CacheNorthstarTotals: CacheNorthstarTotals{
			Requests: 10, PromptTokens: 1000, InputTokens: 1000,
		}},
	}
	prices := northstarPrices(rows)
	for _, tc := range []struct {
		view               string
		saved, opportunity float64
	}{
		{"list", 0.113, 0.02825}, {"charged", 0.1555, 0.038875}, {"upstream", 0.071, 0.01775},
	} {
		t.Run(tc.view, func(t *testing.T) {
			result := northstarCost(rows, rows, prices, tc.view)
			require.Equal(t, "USD", result.Currency)
			require.Equal(t, "settled_cost", result.PricingSource)
			require.Equal(t, 2, result.PricedModels)
			require.InDelta(t, 0.9, result.PricedPromptShare, 1e-12)
			require.NotNil(t, result.SavedAmount)
			require.InDelta(t, tc.saved, *result.SavedAmount, 1e-10)
			require.NotNil(t, result.OpportunityAmount)
			require.InDelta(t, tc.opportunity, *result.OpportunityAmount, 1e-10)
			require.NotNil(t, result.HitUpper)
			require.InDelta(t, 0.113/0.1695, *result.HitUpper, 1e-12)
			require.NotNil(t, result.HitLower)
			require.InDelta(t, 0.056/0.1695, *result.HitLower, 1e-12)
			require.True(t, result.PricingChangedInWindow)
			require.Contains(t, result.Notes, "partial_pricing")
			require.Contains(t, result.Notes, "pricing_changed_in_window")
			require.Contains(t, result.Notes, "component_mismatch")
			require.Contains(t, result.Notes, "forced_cache_billing_candidates")
			if tc.view == "upstream" {
				require.NotNil(t, result.UpstreamCoveredPromptShare)
				require.InDelta(t, 2.0/3, *result.UpstreamCoveredPromptShare, 1e-12)
				require.Contains(t, result.Notes, "upstream_partial_coverage")
			} else {
				require.Nil(t, result.UpstreamCoveredPromptShare)
			}
			if tc.view == "list" {
				require.NotContains(t, result.Notes, "cost_weighted_hit_uses_list")
			} else {
				require.Contains(t, result.Notes, "cost_weighted_hit_uses_list")
			}
		})
	}
	require.InDelta(t, 14, northstarPrices(rows)[cacheNorthstarModel{PlatformOpenAI, "gpt-5.4"}].weight*1e6, 1e-12)
}

func TestCacheNorthstarUnknownPriceIsNull(t *testing.T) {
	rows := []CacheNorthstarRow{{Platform: PlatformOpenAI, Model: "no-read-price", CacheNorthstarTotals: CacheNorthstarTotals{PromptTokens: 1000, InputTokens: 1000, InputCostMicros: 1000}}}
	result := northstarCost(rows, rows, northstarPrices(rows), "list")
	require.Zero(t, result.PricedPromptShare)
	require.Zero(t, result.PricedModels)
	require.Nil(t, result.SavedAmount)
	require.Nil(t, result.OpportunityAmount)
	require.Nil(t, result.HitUpper)
	require.Nil(t, result.HitLower)
}

func TestCacheNorthstarPriceChangeIgnoresTinyModels(t *testing.T) {
	now := time.Now()
	rows := []CacheNorthstarRow{
		{BucketStart: now, Platform: PlatformOpenAI, Model: "main", CacheNorthstarTotals: CacheNorthstarTotals{PromptTokens: 100000, InputTokens: 50000, ReadTokens: 50000, InputCostMicros: 500000, ReadCostMicros: 50000}},
		{BucketStart: now, Platform: PlatformOpenAI, Model: "tiny", CacheNorthstarTotals: CacheNorthstarTotals{PromptTokens: 100, InputTokens: 50, ReadTokens: 50, InputCostMicros: 1000, ReadCostMicros: 100}},
		{BucketStart: now.Add(time.Hour), Platform: PlatformOpenAI, Model: "tiny", CacheNorthstarTotals: CacheNorthstarTotals{PromptTokens: 100, InputTokens: 50, ReadTokens: 50, InputCostMicros: 2000, ReadCostMicros: 100}},
	}
	result := northstarCost(rows, rows, northstarPrices(rows), "list")
	require.False(t, result.PricingChangedInWindow)
}

// R17 counts priced models only when they participate in the strict result;
// pricing coverage still includes all business rows and is independent of view.
func TestCacheNorthstarPricedModelsUseStrictScope(t *testing.T) {
	hour := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	row := CacheNorthstarRow{BucketStart: hour, Platform: PlatformOpenAI, Model: "gpt-shared", ThresholdBand: 1024,
		CacheNorthstarTotals: CacheNorthstarTotals{Requests: 10, PromptTokens: 12000, InputTokens: 6000, ReadTokens: 6000, InputCostMicros: 60000, ReadCostMicros: 6000}}
	otherPlatform := row
	otherPlatform.Platform = PlatformAnthropic // Same model name is a distinct price key.
	below := row
	below.Model, below.ThresholdBand = "gpt-below", 512
	unknown := row
	unknown.Model = "unknown-threshold"
	unpriced := row
	unpriced.Model, unpriced.ReadTokens, unpriced.InputTokens = "gpt-no-read", 0, 12000
	unpriced.ReadCostMicros = 0
	policy := parseNorthstarPolicy(northstarPolicyDefaults())
	meta := CachePanelMeta{PolicyVersion: policy.PolicyVersion, AsOf: hour.Add(time.Hour), GeneratedAt: hour.Add(time.Hour), Window: CachePanelWindow{Start: hour, End: hour.Add(time.Hour)}}
	for _, view := range []string{"list", "charged", "upstream"} {
		t.Run(view, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				rows       []CacheNorthstarRow
				wantModels int
				wantShare  float64
			}{
				{"mixed", []CacheNorthstarRow{row, otherPlatform, below, unknown, unpriced}, 2, 0.8},
				{"business_only", []CacheNorthstarRow{below, unknown}, 0, 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got := buildCacheNorthstar(meta, CachePanelQuery{Window: "24h", Granularity: "1h", Platform: "all", CostView: view}, policy,
						&CacheNorthstarState{PolicyChangedAt: hour}, CacheNorthstarData{Rows: tc.rows, Hours: []CacheNorthstarHour{{BucketStart: hour}}}, nil)
					require.Equal(t, tc.wantModels, got.Summary.Cost.PricedModels)
					require.InDelta(t, tc.wantShare, got.Summary.Cost.PricedPromptShare, 1e-12)
					if tc.wantModels == 0 {
						require.Nil(t, got.Summary.Cost.SavedAmount)
						require.Nil(t, got.Summary.Cost.OpportunityAmount)
					}
				})
			}
		})
	}
}
