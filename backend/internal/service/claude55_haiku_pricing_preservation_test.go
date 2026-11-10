//go:build unit

package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

const haiku55PricingKey = "claude-haiku-5-5"

func bundledHaikuPricing(t *testing.T) ([]byte, map[string]*LiteLLMModelPricing) {
	t.Helper()
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	data, err := (&PricingService{}).parsePricingData(body)
	require.NoError(t, err)
	return body, data
}

func TestClaudeHaiku55RemoteCatalogPreservesExistingAliasPricing(t *testing.T) {
	body, remoteData := bundledHaikuPricing(t)
	require.Contains(t, remoteData, haiku55PricingKey)
	require.Contains(t, remoteData, "claude-3-haiku-20240307")
	catalog := &PricingService{pricingData: remoteData}
	billing := NewBillingService(&config.Config{}, catalog)

	legacyCatalogPrice := catalog.GetModelPricing("claude-3-haiku-20240307")
	require.NotNil(t, legacyCatalogPrice)
	legacyBillingPrice, err := billing.GetModelPricing("claude-3-haiku-20240307")
	require.NoError(t, err)

	for _, model := range []string{
		"anthropic/claude-haiku-5.5",
		"anthropic.claude-haiku-5-5",
		"anthropic.claude-haiku-5.5",
		"us.anthropic.claude-haiku-5-5",
		"claude-haiku-5.5",
	} {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, legacyCatalogPrice, catalog.GetModelPricing(model))
			got, err := billing.GetModelPricing(model)
			require.NoError(t, err)
			require.Equal(t, legacyBillingPrice, got)
		})
	}

	canonical, err := billing.GetModelPricing(haiku55PricingKey)
	require.NoError(t, err)
	require.Equal(t, remoteData[haiku55PricingKey].InputCostPerToken, canonical.InputPricePerToken)
	require.Equal(t, remoteData[haiku55PricingKey].OutputCostPerToken, canonical.OutputPricePerToken)
	require.NotEqual(t, legacyBillingPrice, canonical)
	require.NotEmpty(t, body)
}

func TestClaudeHaiku55BuiltInPriceFillsMissingRemoteEntry(t *testing.T) {
	remoteBody, remoteData := bundledHaikuPricing(t)
	delete(remoteData, haiku55PricingKey)
	remoteWithoutHaiku55, err := json.Marshal(remoteData)
	require.NoError(t, err)

	fallbackPath := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	cfg := &config.Config{Pricing: config.PricingConfig{FallbackFile: fallbackPath}}
	service := &PricingService{cfg: cfg}
	merged, _, err := service.buildPricingData(remoteWithoutHaiku55)
	require.NoError(t, err)

	bundled, err := service.parsePricingData(remoteBody)
	require.NoError(t, err)
	require.Equal(t, bundled[haiku55PricingKey], merged[haiku55PricingKey])

	billing := NewBillingService(cfg, &PricingService{pricingData: merged})
	pricing, err := billing.GetModelPricing(haiku55PricingKey)
	require.NoError(t, err)
	require.Equal(t, bundled[haiku55PricingKey].InputCostPerToken, pricing.InputPricePerToken)
	require.Equal(t, bundled[haiku55PricingKey].OutputCostPerToken, pricing.OutputPricePerToken)
	require.Equal(t, 100_000, pricing.LongContextInputThreshold)
}
