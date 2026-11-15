//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBillingPricingProvenance(t *testing.T) {
	catalog := newStubPricingServiceFromJSON(t, `{"fixture":{"input_cost_per_token":0.001,"output_cost_per_token":0.002},"claude-opus-4.5-20251101":{"input_cost_per_token":0.001,"output_cost_per_token":0.002},"dated-20251101":{"input_cost_per_token":0.001,"output_cost_per_token":0.002},"gpt-5.4":{"input_cost_per_token":0.003,"output_cost_per_token":0.004},"cheap":{"input_cost_per_token":0.0001,"output_cost_per_token":0.0002}}`)
	catalog.localHash = "body-sha"
	catalog.customFilesHash = "override-sha"
	for _, tc := range []struct{ model, key, match string }{
		{"fixture", "fixture", "exact"}, {"claude-opus-4-5-20251101", "claude-opus-4.5-20251101", "variant"},
		{"dated-20260101", "dated-20251101", "base"}, {"gpt-9876", "gpt-5.4", "default"}, {"unknown", "", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			price, source := catalog.lookupModelPricingWithSource(tc.model)
			if tc.key == "" {
				require.Nil(t, price)
				require.Empty(t, source.ModelKey)
				return
			}
			require.NotNil(t, price)
			require.Equal(t, tc.key, source.ModelKey)
			require.Equal(t, tc.match, source.Match)
			require.Equal(t, "body-sha:override-sha", source.Revision)
		})
	}
	for _, origin := range []string{PricingSourceGroup, PricingSourceChannel} {
		t.Run(origin, func(t *testing.T) {
			card := ChannelModelPricing{Platform: PlatformOpenAI, Models: []string{"gpt-9876"}, InputPrice: new(float64(0.01))}
			billing, resolver := newTokenCostTestEnv(t, PlatformOpenAI, []ChannelModelPricing{card}, catalog)
			group := &Group{ID: 100, Platform: PlatformOpenAI}
			if origin == PricingSourceGroup {
				group.ModelPricing = []ChannelModelPricing{card}
			}
			cost, err := billing.CalculateCostUnified(CostInput{Purpose: "billing", Ctx: context.Background(), Model: "gpt-9876", GroupID: &group.ID, Group: group, Tokens: UsageTokens{InputTokens: 10, OutputTokens: 10}, RateMultiplier: 1, Resolver: resolver})
			require.NoError(t, err)
			require.InDelta(t, 0.14, cost.TotalCost, 1e-12)
			source := decodePricingSource(cost.PricingSource)
			require.Equal(t, "gpt-5.4", source.ModelKey)
			require.Equal(t, "catalog", source.Source)
			require.Contains(t, source.Overrides["input"], origin+":")
			require.NotContains(t, source.Overrides, "output")
			require.Equal(t, 0.01, source.EffectivePrices["input"])
			require.Equal(t, 0.004, source.EffectivePrices["output"])
		})
	}
	for _, adopt := range []bool{false, true} {
		t.Run(map[bool]string{true: "response_adopted", false: "response_rejected"}[adopt], func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.billingService = NewBillingService(&config.Config{}, catalog)
			baseline, response := "cheap", "fixture"
			if adopt {
				baseline, response = response, baseline
			}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "source-test", Model: baseline, UpstreamModel: baseline, UpstreamResponseModel: response, Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 10}}, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3}, ChannelUsageFields: ChannelUsageFields{BillingModelSource: BillingModelSourceResponse}})
			require.NoError(t, err)
			require.NotNil(t, repo.lastLog)
			require.Equal(t, "cheap", decodePricingSource(repo.lastLog.PricingSource).ModelKey)
			snapshot := BillingUsageSnapshot(repo.lastLog)
			frozen := append([]byte(nil), snapshot.PricingSource...)
			repo.lastLog.PricingSource[0] = '!'
			require.Equal(t, frozen, []byte(snapshot.PricingSource))
			raw, err := json.Marshal(&UsageBillingCommand{Usage: snapshot})
			require.NoError(t, err)
			var restored UsageBillingCommand
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.JSONEq(t, string(frozen), string(restored.Usage.PricingSource))
		})
	}
	t.Run("lookup_purposes", func(t *testing.T) {
		var buf bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(previous)
		billing := NewBillingService(&config.Config{}, catalog)
		resolver := NewModelPricingResolver(nil, billing)
		for _, purpose := range []string{"display", "estimate", "reservation"} {
			_, err := billing.CalculateCostUnified(CostInput{Purpose: purpose, Model: "gpt-9876", Tokens: UsageTokens{InputTokens: 1}, RateMultiplier: 1, Resolver: resolver})
			require.NoError(t, err)
		}
		_, err := billing.GetEstimatedCost("gpt-9876", 1, 1)
		require.NoError(t, err)
		require.Empty(t, buf.String())
		cost, err := billing.CalculateCostUnified(CostInput{Purpose: "billing", Model: "gpt-9876", Tokens: UsageTokens{InputTokens: 1}, RateMultiplier: 1, Resolver: resolver})
		require.NoError(t, err)
		billing.warnBillingPricingFallback(cost.PricingSource)
		require.Contains(t, buf.String(), "Using fallback pricing")
		require.Contains(t, buf.String(), "gpt-5.4")
	})
	t.Run("image_and_request", func(t *testing.T) {
		billing := NewBillingService(&config.Config{}, catalog)
		image := billing.CalculateImageCost("fixture", "1K", 2, &ImagePriceConfig{Price1K: new(float64(0.25))}, 1)
		require.Equal(t, 0.5, image.TotalCost)
		require.Equal(t, 0.25, decodePricingSource(image.PricingSource).EffectivePrices["per_image"])
		group := &Group{ID: 1, ModelPricing: []ChannelModelPricing{{Models: []string{"fixture"}, BillingMode: BillingModePerRequest, PerRequestPrice: new(float64(0.3))}}}
		cost, err := billing.CalculateCostUnified(CostInput{Purpose: "billing", Ctx: context.Background(), Model: "fixture", Group: group, RequestCount: 2, RateMultiplier: 1, Resolver: NewModelPricingResolver(nil, billing), PricingAt: time.Now()})
		require.NoError(t, err)
		require.Equal(t, 0.6, cost.TotalCost)
		require.Equal(t, 0.3, decodePricingSource(cost.PricingSource).EffectivePrices["per_request"])
	})
}
