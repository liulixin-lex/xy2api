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
