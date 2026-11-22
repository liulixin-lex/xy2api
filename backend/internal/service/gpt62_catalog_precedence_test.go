//go:build unit

package service

import (
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGPT62RemoteExactSpellingBeatsMergedFallback(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		for _, prefix := range []string{"openai/", "models/", "azure/", "OpenAI/"} {
			slug := prefix + model
			t.Run(slug, func(t *testing.T) {
				cfg := &config.Config{Pricing: config.PricingConfig{FallbackFile: filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")}}
				raw := []byte(fmt.Sprintf(`{%q:{"input_cost_per_token":0.000031,"output_cost_per_token":0.000059}}`, strings.ToLower(slug)))
				ps := &PricingService{cfg: cfg}
				data, _, origins, err := ps.buildPricingDataWithSource(raw, "remote")
				require.NoError(t, err)
				ps.pricingData, ps.pricingOrigins = data, origins
				expected := ps.pricingData[strings.ToLower(slug)]
				require.Same(t, expected, ps.GetModelPricing(slug))
				require.Same(t, expected, ps.GetIdentifiedModelPricing(slug))
				_, source := ps.lookupModelPricingWithSource(slug)
				require.Equal(t, strings.ToLower(slug), source.ModelKey)
				require.Equal(t, "remote", source.Source)
				b := NewBillingService(cfg, ps)
				c, err := b.CalculateCost(slug, UsageTokens{InputTokens: 1000, OutputTokens: 1000}, .5)
				require.NoError(t, err)
				require.InDelta(t, .09, c.TotalCost, 1e-12)
				require.InDelta(t, .045, c.ActualCost, 1e-12)
				gid := int64(1)
				cs := newChannelServiceWithPricings(gid, nil)
				repo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.billingService = b
				svc.resolver = NewModelPricingResolver(cs, b)
				svc.channelService = cs
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: float64Ptr(.4)}
				group := &Group{ID: gid, Platform: PlatformOpenAI, RateMultiplier: .5, Hydrated: true, Status: StatusActive}
				result := &OpenAIForwardResult{Model: slug, UpstreamModel: slug, Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 1000}}
				err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 1, Group: group, GroupID: &gid}, User: &User{ID: 1}, Account: account})
				require.NoError(t, err)
				require.InDelta(t, .09, repo.lastLog.TotalCost, 1e-12)
				require.InDelta(t, .045, repo.lastLog.ActualCost, 1e-12)
				require.NotNil(t, repo.lastLog.AccountStatsCost)
				require.InDelta(t, .09, *repo.lastLog.AccountStatsCost, 1e-12)
				at := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
				buy := openAITierCatalogPurchase(context.Background(), b, account, slug, "default", at, false)
				buy.Tokens = UsageTokens{InputTokens: 1000, OutputTokens: 1000}
				purchase, err := b.CalculateCostUnified(buy)
				require.NoError(t, err)
				require.InDelta(t, .09, purchase.TotalCost, 1e-12)
				group.ModelPricing = []ChannelModelPricing{{Models: []string{slug}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, InputPrice: float64Ptr(20e-6), OutputPrice: float64Ptr(100e-6), CacheReadPrice: float64Ptr(.4e-6), CacheWritePrice: float64Ptr(10e-6)}}
				resolver := NewModelPricingResolver(nil, b)
				sell := CostInput{Ctx: context.Background(), Model: slug, Group: group, GroupID: &gid, Resolver: resolver, RateMultiplier: 1}
				sell.Resolved = resolver.Resolve(sell.Ctx, PricingInput{Model: slug, Group: group, GroupID: &gid})
				gate := &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, threshold: .5, ceilingOn: true, pricingAt: at}
				quote := quoteOpenAITierProfit(b, gate, sell, buy, nil, account.ID, gid, false)
				require.True(t, quote.valid)
				require.InDelta(t, 10.0/31, quote.ceiling, 1e-10)
				require.Equal(t, openAIProfitFilterReasonThreshold, openAITierProfitVerdict(context.Background(), gate, account, quote))
				// When both keys came from remote, the established canonical-first order wins.
				ps.pricingData[model] = &LiteLLMModelPricing{InputCostPerToken: 7e-6, OutputCostPerToken: 11e-6}
				ps.pricingOrigins[model] = PricingProvenance{Source: "remote"}
				require.Same(t, ps.pricingData[model], ps.GetModelPricing(slug))
				// A patched fallback card is an operator override, not a fallback-only card.
				ps.pricingOrigins[model] = PricingProvenance{Source: "fallback_file", Overrides: map[string]string{"input_cost_per_token": "override_file:fixture"}}
				require.Same(t, ps.pricingData[model], ps.GetModelPricing(slug))
				overrideFile := filepath.Join(t.TempDir(), "override.json")
				require.NoError(t, os.WriteFile(overrideFile, []byte(fmt.Sprintf(`{%q:{"input_cost_per_token":0.000013,"output_cost_per_token":0.000017}}`, model)), 0600))
				ps.cfg.Pricing.OverrideFile = overrideFile
				data, _, origins, err = ps.buildPricingDataWithSource(raw, "remote")
				require.NoError(t, err)
				ps.pricingData, ps.pricingOrigins = data, origins
				require.Equal(t, 13e-6, ps.GetModelPricing(slug).InputCostPerToken)
				require.Equal(t, 17e-6, ps.GetIdentifiedModelPricing(slug).OutputCostPerToken)
				_, source = ps.lookupModelPricingWithSource(slug)
				require.Equal(t, model, source.ModelKey)
				require.NotEmpty(t, source.Overrides)

			})
		}
	}
}
