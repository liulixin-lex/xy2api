package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAITierCatalogPurchasePromotion(t *testing.T) {
	bs := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-6.2-astra": {InputCostPerToken: 1e-6}, "gpt-6.2-sol": {InputCostPerToken: 1e-6}, "gpt-6.1-sol": {InputCostPerToken: 1e-6},
	}})
	cutover := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	official := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	thirdParty := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://vendor.example/v1"}}
	for _, tt := range []struct {
		model   string
		at      time.Time
		account *Account
		mult    float64
	}{
		{"gpt-6.2-astra", cutover.Add(-time.Nanosecond), official, 3},
		{"gpt-6.2-sol", cutover, official, 6},
		{"gpt-6.1-sol", cutover.Add(-time.Hour), official, 6},
		{"gpt-6.2-astra", cutover, thirdParty, 2},
	} {
		q := openAITierCatalogPurchase(context.Background(), bs, tt.account, tt.model, "ultrafast", tt.at, false)
		q.Tokens = UsageTokens{InputTokens: 1000}
		ultra, err := bs.CalculateCostUnified(q)
		require.NoError(t, err)
		q.ServiceTier = "default"
		base, err := bs.CalculateCostUnified(q)
		require.NoError(t, err)
		require.Positive(t, base.TotalCost)
		require.InDelta(t, tt.mult, ultra.TotalCost/base.TotalCost, 1e-9)
	}
}

func TestOpenAITierProfitCandidateAndSlotRecheck(t *testing.T) {
	bs := NewBillingService(nil, nil)
	service := &OpenAIGatewayService{billingService: bs}
	const model = "gpt-6-astra"
	mult := 4.5
	group := profitControlTestGroup(77, 0, 0)
	group.OpenAITierPolicyVersion = OpenAITierPolicyExplicit
	group.ModelPricing = []ChannelModelPricing{{Models: []string{model}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, UltrafastMultiplier: &mult}}
	ctx := withOpenAITierProfitInput(profitControlTestCtx(group), []byte(`{"model":"gpt-6-astra"}`))
	gate := &openAIProfitControlGate{groupID: group.ID, platform: PlatformOpenAI, threshold: 0.2, sell: 0.2, ceilingOn: true, pricingAt: time.Now()}
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, gate)
	costRate := 0.15
	acc := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: &costRate}
	prices := &openAITierPrices{billing: bs, at: gate.pricingAt, group: group}
	d := &OpenAITierDecision{PolicyVersion: 2, OutboundTier: "ultrafast", Source: "group", FreeUltrafast: true, prices: prices}
	require.Equal(t, openAIProfitFilterReasonThreshold, service.openAITierCandidateProfitReason(ctx, acc, model, d))
	veto, reason := OpenAIProfitControlVeto(ctx, acc)
	require.True(t, veto)
	require.Equal(t, openAIProfitFilterReasonThreshold, reason)
	d.FreeUltrafast = false
	require.Empty(t, service.openAITierCandidateProfitReason(ctx, acc, model, d))
	costRate = 0.16
	veto, reason = OpenAIProfitControlVeto(ctx, acc)
	require.True(t, veto)
	require.Equal(t, openAIProfitFilterReasonThreshold, reason)
	costRate = 0.01
	gate.floor = 0.02
	veto, reason = OpenAIProfitControlVeto(ctx, acc)
	require.True(t, veto)
	require.Equal(t, openAIProfitFilterReasonFloor, reason)
	acc.AccountGroups = []AccountGroup{{GroupID: 77, Pinned: true}}
	require.Empty(t, service.openAITierCandidateProfitReason(ctx, acc, model, d))
	veto, _ = OpenAIProfitControlVeto(ctx, acc)
	require.False(t, veto)
}

func TestOpenAITierProfitUsesSeparatePurchaseRates(t *testing.T) {
	bs := NewBillingService(nil, nil)
	const model = "gpt-6-astra"
	ctx := context.Background()
	buy := openAITierCatalogPurchase(ctx, bs, nil, model, "ultrafast", time.Now(), false)
	sell := buy
	sellCard, buyCard := *buy.Resolved, *buy.Resolved
	sellBase := ModelPricing{InputPricePerToken: 1, OutputPricePerToken: 1, CacheReadPricePerToken: 1}
	buyBase := ModelPricing{InputPricePerToken: 0.5, OutputPricePerToken: 2, CacheReadPricePerToken: 1}
	mult := 2.0
	sellBase.UltrafastMultiplierOverride = &mult
	buyBase.UltrafastMultiplierOverride = &mult
	sellCard.BasePricing, buyCard.BasePricing = &sellBase, &buyBase
	sell.Resolved, buy.Resolved = &sellCard, &buyCard
	gate := &openAIProfitControlGate{threshold: 1}
	q := quoteOpenAITierProfit(bs, gate, sell, buy, nil, 1, 3, false)
	require.True(t, q.valid)
	require.InDelta(t, 0.5, q.ceiling, 1e-8) // output is the limiting component
	channel := &Channel{ApplyPricingToAccountStats: true}
	q = quoteOpenAITierProfit(bs, gate, sell, buy, channel, 1, 3, true)
	require.True(t, q.valid)
	require.InDelta(t, 0.5, q.ceiling, 1e-8) // mirror is the pre-waiver retail total
	high := 8.0
	channel.AccountStatsPricingRules = []AccountStatsPricingRule{{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{
		Models: []string{model}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, Intervals: []PricingInterval{{MinTokens: 1000, OutputPrice: &high}},
	}}}}
	q = quoteOpenAITierProfit(bs, gate, sell, buy, channel, 1, 3, false)
	require.True(t, q.valid)
	require.InDelta(t, 0.25, q.ceiling, 1e-8) // long output can enter the expensive purchase tier with short input
}

func TestOpenAITierProcurementHonorsZeroContractAndPreWaiverMirror(t *testing.T) {
	bs := NewBillingService(nil, nil)
	channel := Channel{ID: 42, Status: StatusActive, GroupIDs: []int64{3}, ApplyPricingToAccountStats: true}
	zero := 0.0
	channel.AccountStatsPricingRules = []AccountStatsPricingRule{{AccountIDs: []int64{11}, Pricing: []ChannelModelPricing{{
		Platform: PlatformOpenAI, Models: []string{"gpt-6"}, BillingMode: BillingModeToken, InputPrice: &zero,
	}}}}
	cache := populateChannelCache([]Channel{channel}, map[int64]string{3: PlatformOpenAI})
	at := time.Now()
	ctx := context.WithValue(context.Background(), openAITierPricesKey{}, &openAITierPrices{billing: bs, channels: cache, at: at})
	tier := "ultrafast"
	cred := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, tt := range []struct {
		id   int64
		want float64
	}{{11, 0}, {12, 4.5}} {
		log := &UsageLog{ServiceTier: &tier, ActualCost: 0.2}
		applyAccountStatsCost(ctx, log, &ChannelService{}, bs, tt.id, 3, "gpt-6", "gpt-6", UsageTokens{InputTokens: 1000}, 4.5, at, false, cred)
		require.NotNil(t, log.AccountStatsCost)
		require.Equal(t, tt.want, *log.AccountStatsCost)
	}
}
