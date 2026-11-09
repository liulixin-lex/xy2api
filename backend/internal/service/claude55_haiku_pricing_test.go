//go:build unit

package service

import (
	"os"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Haiku 5.5 bills $0.10/$0.50 per MTok while the prompt (input + cache read +
// cache write) is at most 100K tokens and $0.50/$2.50 above; cache reads and
// writes follow the input rate. The bundled entry mirrors the remote catalog;
// without either it used to fall into the "haiku" family and the
// claude-3-haiku card ($0.25/$1.25).
func TestClaudeHaiku55PricingTwoRateCards(t *testing.T) {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(data)
	require.NoError(t, err)
	sources := map[string]*BillingService{
		"billing fallback": newTestBillingService(),
		"pricing fallback": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"claude-3-haiku-20240307": {InputCostPerToken: 0.25e-6, OutputCostPerToken: 1.25e-6},
		}}),
		"catalog": NewBillingService(&config.Config{}, catalog),
	}
	for source, svc := range sources {
		for _, model := range []string{"claude-haiku-5-5"} {
			t.Run(source+"/"+model, func(t *testing.T) {
				for _, prompt := range []int{100_000, 100_001} {
					tokens := UsageTokens{
						InputTokens: prompt - 3000, OutputTokens: 500,
						CacheReadTokens: 2000, CacheCreationTokens: 1000,
						CacheCreation5mTokens: 400, CacheCreation1hTokens: 600,
					}
					cost, err := svc.CalculateCost(model, tokens, 1)
					require.NoError(t, err)
					m := 1.0
					if prompt > 100_000 {
						m = 5
					}
					require.InDelta(t, float64(tokens.InputTokens)*0.1e-6*m, cost.InputCost, 1e-12)
					require.InDelta(t, (400*0.125e-6+600*0.2e-6)*m, cost.CacheCreationCost, 1e-12)
					require.InDelta(t, 2000*0.01e-6*m, cost.CacheReadCost, 1e-12)
					require.InDelta(t, 500*0.5e-6*m, cost.OutputCost, 1e-12)
					require.Equal(t, prompt > 100_000, cost.LongContextBillingApplied)
				}
			})
		}
	}

	// Other Haiku models keep their cards.
	pricing, err := sources["catalog"].GetModelPricing("claude-haiku-4-5")
	require.NoError(t, err)
	require.Equal(t, 1e-6, pricing.InputPricePerToken)
	require.Zero(t, pricing.LongContextInputThreshold)
	pricing, err = sources["billing fallback"].GetModelPricing("claude-3-haiku-20240307")
	require.NoError(t, err)
	require.Equal(t, 0.25e-6, pricing.InputPricePerToken)
}
