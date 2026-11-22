//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGPT62PricingCatalogPrecedenceAndProvenance(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		t.Run(model, func(t *testing.T) {
			// Operator data can differ from published prices, including explicit zeros
			// and a Fast ratio other than 2. Existing catalog entries must keep it all.
			for _, rate := range []float64{0, 9e-6} {
				card := &LiteLLMModelPricing{InputCostPerToken: rate, InputCostPerTokenPriority: 3 * rate, OutputCostPerToken: 5 * rate, OutputCostPerTokenPriority: 15 * rate, CacheCreationInputTokenCost: rate, CacheCreationInputTokenCostPriority: 3 * rate, CacheReadInputTokenCost: rate / 10, CacheReadInputTokenCostPriority: 3 * rate / 10}
				catalog := &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: card}}
				got, source := catalog.lookupModelPricingWithSource(model)
				require.Same(t, card, got)
				require.Equal(t, model, source.ModelKey)
				require.Equal(t, "exact", source.Match)
				require.Equal(t, "catalog", source.Source)
				billing := NewBillingService(nil, catalog)
				p, err := billing.GetModelPricing(model)
				require.NoError(t, err)
				require.Equal(t, rate, p.InputPricePerToken)
				require.Equal(t, 3*rate, p.InputPricePerTokenPriority)
				require.Equal(t, rate, p.CacheCreationPricePerToken)
				require.Equal(t, 3*rate, p.CacheCreationPricePerTokenPriority)
				cost, err := billing.CalculateCostWithServiceTier(model, UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 100, CacheCreationTokens: 100}, .5, "priority")
				require.NoError(t, err)
				require.InDelta(t, 4830*rate, cost.TotalCost, 1e-12)
				require.InDelta(t, 2415*rate, cost.ActualCost, 1e-12)
				// Alias resolution retains this repository's pricing_source key/match.
				for _, suffix := range []string{"-high", "-openai-compact"} {
					got, source = catalog.lookupModelPricingWithSource(model + suffix)
					require.Same(t, card, got)
					require.Equal(t, model, source.ModelKey)
					require.Equal(t, "openai_variant", source.Match)
					require.Equal(t, "catalog", source.Source)
				}
			}
		})
	}
}

func TestGPT62MissingCatalogPricing(t *testing.T) {
	for _, tc := range []struct {
		model                      string
		input, output, read, write float64
	}{
		{"gpt-6.2-sol", 2e-6, 10e-6, .1e-6, 2.5e-6},
		{"gpt-6.2-astra", 8e-6, 40e-6, .4e-6, 10e-6},
	} {
		for _, catalog := range []*PricingService{nil, {pricingData: map[string]*LiteLLMModelPricing{}}} {
			b := NewBillingService(nil, catalog)
			for _, suffix := range []string{"", "-high", "-openai-compact"} {
				p, raw, err := b.getModelPricingAtWithSource(tc.model+suffix, time.Time{})
				require.NoError(t, err)
				require.Equal(t, tc.input, p.InputPricePerToken)
				require.Equal(t, tc.output, p.OutputPricePerToken)
				require.Equal(t, tc.read, p.CacheReadPricePerToken)
				require.Equal(t, tc.write, p.CacheCreationPricePerToken)
				require.Equal(t, 272000, p.LongContextInputThreshold)
				source := decodePricingSource(raw)
				require.Equal(t, tc.model, source.ModelKey)
				require.Equal(t, "builtin", source.Source)
			}
		}
	}
}

func TestGPT62BundledPricesOnlyFillMissingRemoteModels(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	catalog := &PricingService{cfg: &config.Config{Pricing: config.PricingConfig{FallbackFile: path}}}
	bundled, err := catalog.parsePricingData(body)
	require.NoError(t, err)
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		require.Contains(t, bundled, model)
		remote := []byte(`{"` + model + `":{"input_cost_per_token":0,"output_cost_per_token":0}}`)
		merged, _, err := catalog.buildPricingData(remote)
		require.NoError(t, err)
		require.Zero(t, merged[model].InputCostPerToken)
		require.Zero(t, merged[model].OutputCostPerToken)
		merged, _, err = catalog.buildPricingData([]byte(`{"unrelated":{"input_cost_per_token":1,"output_cost_per_token":1}}`))
		require.NoError(t, err)
		require.Equal(t, bundled[model], merged[model])
	}
}

// These are the intentionally repaired cases. Exact catalog prices and
// per-request cards have separate preservation assertions below.
func TestGPT62RepairedTokenBillingAndChannelAliases(t *testing.T) {
	ctx := context.Background()
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		scale := 1.0
		if model == "gpt-6.2-astra" {
			scale = 4
		}
		for _, catalog := range []*PricingService{nil, {pricingData: map[string]*LiteLLMModelPricing{}}} {
			b := NewBillingService(nil, catalog)
			for _, slug := range []string{model, model + "-high", model + "-openai-compact"} {
				cost, err := b.CalculateCost(slug, UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 100, CacheCreationTokens: 100}, .5)
				require.NoError(t, err)
				require.InDelta(t, .00326*scale, cost.TotalCost, 1e-12, slug)
				require.InDelta(t, .00163*scale, cost.ActualCost, 1e-12, slug)
				cost, err = b.CalculateCost(slug, UsageTokens{InputTokens: 300000, OutputTokens: 100, CacheReadTokens: 100, CacheCreationTokens: 100}, 1)
				require.NoError(t, err)
				require.InDelta(t, 1.20202*scale, cost.TotalCost, 1e-12, slug)
				require.True(t, cost.LongContextBillingApplied)
			}
		}
		// Give the remote exact ID a distinct price to prove that the channel wins
		// for recognized suffixes and provider-prefixed names, including zero cards.
		b := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: {InputCostPerToken: 9e-6, OutputCostPerToken: 9e-6}}})
		for _, price := range []float64{0, 1e-6} {
			gid := int64(3)
			card := ChannelModelPricing{Models: []string{model}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, InputPrice: &price, OutputPrice: &price, CacheReadPrice: &price, CacheWritePrice: &price, UltrafastMultiplier: float64Ptr(4)}
			r := NewModelPricingResolver(newChannelServiceWithPricings(gid, []ChannelModelPricing{card}), b)
			for _, slug := range []string{model, model + "-high", "openai/" + model, "openai/" + model + "-high"} {
				for _, tier := range []string{"default", "ultrafast"} {
					mult := 1.0
					if tier == "ultrafast" {
						mult = 4
					}
					cost, err := b.CalculateCostUnified(CostInput{Ctx: ctx, Model: slug, GroupID: &gid, Resolver: r, Tokens: UsageTokens{InputTokens: 1000}, RateMultiplier: .5, ServiceTier: tier})
					require.NoError(t, err)
					require.InDelta(t, 1000*price*mult, cost.TotalCost, 1e-12, slug)
					require.InDelta(t, 500*price*mult, cost.ActualCost, 1e-12, slug)
					require.Equal(t, "catalog", decodePricingSource(cost.PricingSource).Source)
					require.Contains(t, decodePricingSource(cost.PricingSource).Overrides["input"], "channel:")
				}
			}
		}
	}
}

func TestGPT62PerRequestCardsPreserved(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		for _, source := range []string{"channel", "group"} {
			for _, price := range []float64{0, .07} {
				b := NewBillingService(nil, nil)
				gid := int64(3)
				card := ChannelModelPricing{Models: []string{model}, Platform: PlatformOpenAI, BillingMode: BillingModePerRequest, PerRequestPrice: &price, FastMultiplier: float64Ptr(2), UltrafastMultiplier: float64Ptr(6)}
				group := &Group{ID: gid, Platform: PlatformOpenAI}
				var cs *ChannelService
				if source == "group" {
					group.ModelPricing = []ChannelModelPricing{card}
				} else {
					cs = newChannelServiceWithPricings(gid, []ChannelModelPricing{card})
				}
				r := NewModelPricingResolver(cs, b)
				for _, tier := range []string{"", "default", "priority", "flex", "ultrafast"} {
					cost, err := b.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: model, GroupID: &gid, Group: group, Resolver: r, RequestCount: 2, Tokens: UsageTokens{InputTokens: 300000}, RateMultiplier: .5, ServiceTier: tier})
					require.NoError(t, err)
					require.InDelta(t, 2*price, cost.TotalCost, 1e-12)
					require.InDelta(t, price, cost.ActualCost, 1e-12)
					require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
				}
			}
		}
	}
}
