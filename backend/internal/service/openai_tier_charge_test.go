//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAITierChargeLedger(t *testing.T) {
	for _, tc := range []struct {
		name, outbound, observed, accountType string
		freeFast, freeUltra, disableFast      bool
		fast, ultra, wantTotal, wantActual    float64
		settled, capReason                    string
	}{
		{"paid fast", "priority", "priority", AccountTypeAPIKey, false, false, false, 2.5, 4.5, 2.5, .5, "priority", ""},
		{"paid ultra", "ultrafast", "ultrafast", AccountTypeAPIKey, false, false, false, 2.5, 4.5, 4.5, .9, "ultrafast", ""},
		{"free ultra", "ultrafast", "ultrafast", AccountTypeAPIKey, false, true, false, 2.5, 4.5, 4.5, .2, "ultrafast", ""},
		{"downgrade cannot raise waived quote", "ultrafast", "priority", AccountTypeAPIKey, false, true, false, 2.5, 4.5, 2.5, .2, "priority", "outbound_quote"},
		{"inverted card", "ultrafast", "priority", AccountTypeAPIKey, false, false, false, 4, 2, 4, .4, "priority", "outbound_quote"},
		{"discount preserved", "priority", "priority", AccountTypeAPIKey, true, false, false, .5, 4.5, .5, .1, "priority", ""},
		{"oauth echo", "ultrafast", "default", AccountTypeOAuth, false, false, false, 2.5, 4.5, 4.5, .9, "ultrafast", ""},
		{"explicit fast downgrade", "priority", "default", AccountTypeAPIKey, false, false, false, 2.5, 4.5, 1, .2, "default", ""},
		{"banned upstream tier", "ultrafast", "priority", AccountTypeAPIKey, false, false, true, 2.5, 4.5, 2.5, .2, "priority", "disabled_response_tier"},
		{"unrequested banned upgrade", "default", "priority", AccountTypeAPIKey, false, false, true, 2.5, 4.5, 1, .2, "default", "disabled_response_tier"},
		{"unrequested upgrade", "default", "ultrafast", AccountTypeAPIKey, false, false, false, 2.5, 4.5, 1, .2, "default", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			gid := int64(7)
			price := .01
			g := &Group{ID: gid, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: .2, OpenAITierPolicyVersion: 2,
				ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-6-astra"}, BillingMode: BillingModeToken, InputPrice: &price,
					FastMultiplier: &tc.fast, UltrafastMultiplier: &tc.ultra}}}
			d := &OpenAITierDecision{ClientPresence: "explicit", ClientTier: tc.outbound, OutboundTier: tc.outbound, PolicyVersion: 2,
				ServingGroupID: gid, FreeFast: tc.freeFast, FreeUltrafast: tc.freeUltra, FastDisabled: tc.disableFast}
			result := &OpenAIForwardResult{RequestID: "tier-charge", Model: "gpt-6-astra", Duration: time.Second,
				Usage: OpenAIUsage{InputTokens: 100}, TierDecision: d, UpstreamResponseServiceTier: tc.observed}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2, GroupID: &gid, Group: g},
				User: &User{ID: 23}, Account: &Account{ID: 88, Platform: PlatformOpenAI, Type: tc.accountType}})
			require.NoError(t, err)
			log := repo.lastLog
			require.NotNil(t, log)
			require.Equal(t, tc.settled, *log.ServiceTier)
			require.InDelta(t, tc.wantTotal, log.TotalCost, 1e-10)
			require.InDelta(t, tc.wantActual, log.ActualCost, 1e-10)
			var info openAITierChargeDetails
			require.NoError(t, json.Unmarshal(log.ServiceTierDetails, &info))
			require.Equal(t, tc.outbound, info.OutboundTier)
			require.Equal(t, tc.capReason, info.CapReason)
			require.Equal(t, "group", info.PriceSource)
			require.InDelta(t, tc.wantActual, info.ActualCost, 1e-10)
		})
	}
}

func TestOpenAITierPricingSnapshotAfterCatalogReload(t *testing.T) {
	b := newTestBillingService()
	catalog := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-6-astra": {InputCostPerToken: .01}}}
	b.pricingService = catalog
	svc := &OpenAIGatewayService{billingService: b}
	ctx := context.WithValue(context.Background(), openAITierPriceStoreKey{}, new(openAITierPriceStore))
	frozen, err := svc.tierPriceSnapshot(ctx)
	require.NoError(t, err)
	catalog.mu.Lock()
	catalog.pricingData = map[string]*LiteLLMModelPricing{"gpt-6-astra": {InputCostPerToken: .5}}
	catalog.mu.Unlock()
	p, err := frozen.billing.GetModelPricing("gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, .01, p.InputPricePerToken)
	retry, err := svc.tierPriceSnapshot(ctx)
	require.NoError(t, err)
	require.Same(t, frozen, retry)
	next, err := svc.tierPriceSnapshot(context.Background())
	require.NoError(t, err)
	p, err = next.billing.GetModelPricing("gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, .5, p.InputPricePerToken)
}

func TestOpenAITierPriceCardScopeAndPrecedence(t *testing.T) {
	const model = "gpt-6-astra"
	gid := int64(100)
	ultra := 4.5
	card := tokenPricingForModels([]string{model}, 1)
	card.UltrafastMultiplier = &ultra
	r := NewModelPricingResolver(newChannelServiceWithPricings(gid, []ChannelModelPricing{card}), newTestBillingService())
	for _, tc := range []struct {
		name           string
		group          *Group
		wantSource     string
		wantMultiplier float64
	}{
		{"selected channel card", &Group{ID: gid}, PricingSourceChannel, 4.5},
		{"matched group inherits model default", &Group{ID: gid, ModelPricing: []ChannelModelPricing{tokenPricingForModels([]string{"gpt-6-*"}, 1)}}, PricingSourceGroup, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithResolvedTargetPlatform(context.Background(), PlatformOpenAI)
			resolved := r.Resolve(ctx, PricingInput{Model: model, Group: tc.group, GroupID: &gid})
			require.Equal(t, tc.wantSource, resolved.Source)
			cost, err := r.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: model, Resolver: r, Resolved: resolved,
				Tokens: UsageTokens{InputTokens: 1_000_000}, RateMultiplier: 1, ServiceTier: "ultrafast"})
			require.NoError(t, err)
			require.InDelta(t, tc.wantMultiplier, cost.TotalCost, 1e-9)
		})
	}
	require.Nil(t, openAITierPriceCard(context.Background(), "gpt-image-1", &card).UltrafastMultiplier)
	require.Nil(t, openAITierPriceCard(context.Background(), "deepseek-v4", &card).UltrafastMultiplier)
	ctx := WithResolvedTargetPlatform(context.Background(), PlatformAnthropic)
	require.Nil(t, openAITierPriceCard(ctx, model, &card).UltrafastMultiplier)
	ctx = context.WithValue(context.Background(), openAITierPricesKey{}, &openAITierPrices{})
	require.Equal(t, &ultra, openAITierPriceCard(ctx, "public-alias", &card).UltrafastMultiplier)
	require.Equal(t, &ultra, card.UltrafastMultiplier)
}

func TestOpenAITierGroupUpdateTakesEffectNextTurn(t *testing.T) {
	old := explicitTierGroup(OpenAITierModeInherit, OpenAITierModeForceOn)
	old.FreeOpenAIUltrafast = true
	key := &APIKey{ID: 1, Group: old, GroupID: &old.ID}
	ctx := WithServingPricingKeySnapshot(context.Background(), key)
	svc := &OpenAIGatewayService{billingService: newTestBillingService()}
	ctx, _ = svc.WithOpenAIRequestPricingContext(ctx, &old.ID)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	first, err := svc.resolveOpenAITier(ctx, account, "gpt-6-astra", []byte("{}"))
	require.NoError(t, err)
	require.Equal(t, "ultrafast", first.OutboundTier)
	require.True(t, first.FreeUltrafast)
	newGroup := *old
	newGroup.OpenAIUltrafastMode = OpenAITierModeForceOff
	newGroup.FreeOpenAIUltrafast = false
	newKey := &APIKey{ID: 1, Group: &newGroup, GroupID: &old.ID}
	ctx = WithServingPricingKeySnapshot(ctx, newKey)
	retry, err := svc.resolveOpenAITier(ctx, account, "gpt-6-astra", []byte("{}"))
	require.NoError(t, err)
	require.Equal(t, first.OutboundTier, retry.OutboundTier)
	require.Same(t, old, retry.prices.group)
	next, _ := svc.WithOpenAITurnPricingContext(ctx, &old.ID)
	updated, err := svc.resolveOpenAITier(next, account, "gpt-6-astra", []byte("{}"))
	require.NoError(t, err)
	require.Equal(t, "default", updated.OutboundTier)
	require.False(t, updated.FreeUltrafast)
	require.Same(t, &newGroup, updated.prices.group)
}

func TestOpenAITierLongContextAndSearchSurcharge(t *testing.T) {
	const model = "gpt-6-astra"
	gid := int64(7)
	g := &Group{ID: gid, Platform: PlatformOpenAI, Hydrated: true, Status: StatusActive, RateMultiplier: .5,
		LongContextPricingEnabled: true, OpenAITierPolicyVersion: 2, FreeOpenAIUltrafast: true, SearchPricePer1k: float64Ptr(50),
		ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{model}, BillingMode: BillingModeToken,
			InputPrice: float64Ptr(2e-6), CacheReadPrice: float64Ptr(2e-6), OutputPrice: float64Ptr(.01), UltrafastMultiplier: float64Ptr(3)}}}
	for _, tc := range []struct {
		name          string
		calls         int
		gross, actual float64
	}{
		{"token only", 0, 5.4, .90},
		{"two separately billed searches", 2, 5.5, .95},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			result := &OpenAIForwardResult{RequestID: "long-tier", Model: model, ServiceTier: stringPtr("ultrafast"), SearchCount: tc.calls,
				Usage: OpenAIUsage{InputTokens: 300_000, CacheReadInputTokens: 100_000, OutputTokens: 40}}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2, GroupID: &gid, Group: g},
				User: &User{ID: 23}, Account: &Account{ID: 88, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}})
			require.NoError(t, err)
			require.InDelta(t, tc.gross, repo.lastLog.TotalCost, 1e-9)
			require.InDelta(t, tc.actual, repo.lastLog.ActualCost, 1e-9)
		})
	}
}

func TestOpenAITierDelayedShadowSettlementUsesDispatchCredential(t *testing.T) {
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	group := explicitTierGroup(OpenAITierModeInherit, OpenAITierModeForceOn)
	group.RateMultiplier = 1
	gid := group.ID
	parent := &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	shadow := &Account{ID: 90, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID}
	result := &OpenAIForwardResult{RequestID: "delayed-shadow", Model: "gpt-6-astra",
		TierDecision: &OpenAITierDecision{OutboundTier: "ultrafast", PolicyVersion: OpenAITierPolicyExplicit,
			prices: &openAITierPrices{billing: svc.billingService, at: time.Now(), group: group, credential: parent}},
		Usage: OpenAIUsage{InputTokens: 100}, UpstreamResponseServiceTier: "default"}
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2, GroupID: &gid, Group: group},
		User: &User{ID: 23}, Account: shadow})
	require.NoError(t, err)
	require.Equal(t, "ultrafast", *repo.lastLog.ServiceTier)
	require.Equal(t, shadow.ID, repo.lastLog.AccountID)
}
