//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestMeteringPreservesSettlement(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini} {
		for _, mirror := range []bool{false, true} {
			for _, force := range []bool{false, true} {
				t.Run(platform+"/"+map[bool]string{false: "contract", true: "mirror"}[mirror]+"/"+map[bool]string{false: "ordinary", true: "compensated"}[force], func(t *testing.T) {
					logs := &openAIRecordUsageLogRepoStub{inserted: true}
					bills := &openAIRecordUsageBillingRepoStub{}
					svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, bills, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
					model := "claude-sonnet-4"
					if platform == PlatformGemini {
						model = "gemini-2.5-pro"
					}
					card := ChannelModelPricing{Models: []string{model}, InputPrice: new(0.01), OutputPrice: new(0.02), CacheReadPrice: new(0.001), CacheWritePrice: new(0.003), CacheWrite1hPrice: new(0.006)}
					channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{10}, ApplyPricingToAccountStats: mirror}
					if !mirror {
						channel.AccountStatsPricingRules = []AccountStatsPricingRule{{AccountIDs: []int64{3}, Pricing: []ChannelModelPricing{card}}}
					}
					svc.channelService = newTestChannelService(makeStandardRepo(channel, map[int64]string{10: platform}))
					svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
					key := &APIKey{ID: 2, GroupID: new(int64(10)), Group: &Group{ID: 10, Platform: platform, RateMultiplier: 1.3, ModelPricing: []ChannelModelPricing{card}}, Quota: 100, RateLimit5h: 100}
					account := &Account{ID: 3, Platform: platform, Type: AccountTypeAPIKey, RateMultiplier: new(0.7), Extra: map[string]any{"quota_limit": 100.0}}
					u := ClaudeUsage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 20, CacheCreationInputTokens: 10, CacheCreationInputTokensPresent: true, CacheCreation5mTokens: 6, CacheCreation1hTokens: 4, CacheCreation5mTokensPresent: true, CacheCreation1hTokensPresent: true, RawPromptTokens: new(130)}
					result := &ForwardResult{RequestID: "metering-fixture", Model: model, Usage: u, Stream: force, ResponseFactsKnown: true, UsagePresent: true, UsageSource: "upstream"}
					at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
					legacy := *result
					if force {
						legacy.Usage.CacheReadInputTokens += legacy.Usage.InputTokens
						legacy.Usage.InputTokens = 0
					}
					wantCost := svc.calculateRecordUsageCost(context.Background(), &legacy, key, model, 1.3, 1.3, at)
					gross := svc.calculateRecordUsageCost(context.Background(), result, key, model, 1.3, 1.3, at)
					wantStats := resolveAccountStatsCost(context.Background(), svc.channelService, svc.billingService, 3, 10, model, UsageTokens{InputTokens: legacy.Usage.InputTokens, OutputTokens: 10, CacheCreationTokens: 10, CacheCreationTokensPresent: true, CacheReadTokens: legacy.Usage.CacheReadInputTokens}, 1, wantCost.TotalCost, "", at, true)
					require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: account, ForceCacheBilling: force, APIKeyService: &APIKeyService{}, PricingAt: at}))
					row, cmd := logs.lastLog, bills.lastCmd
					require.NotNil(t, row)
					require.NotNil(t, cmd)
					require.Equal(t, u, result.Usage)
					require.Equal(t, wantCost.TotalCost, row.TotalCost)
					require.Equal(t, wantCost.ActualCost, row.ActualCost)
					require.Equal(t, wantStats, row.AccountStatsCost)
					for _, got := range []float64{cmd.BalanceCost, cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost} {
						require.Equal(t, QuantizeUsageBillingAmount(wantCost.ActualCost), got)
					}
					require.Zero(t, cmd.SubscriptionCost)
					require.Equal(t, QuantizeUsageBillingAmount(wantCost.TotalCost*0.7), cmd.AccountQuotaCost)
					wantSupplier := 1.262 * 0.7
					if mirror {
						wantSupplier = gross.TotalCost * 0.7
					}
					require.NotNil(t, row.SupplierCostEstimate)
					require.InDelta(t, wantSupplier, *row.SupplierCostEstimate, 1e-12)
					require.Equal(t, *wantStats, legacyUndeliveredAbuseCharge(row, 0.7))
					if force {
						var adjustment meteringAdjustment
						require.NoError(t, json.Unmarshal(row.MeteringAdjustment, &adjustment))
						require.Equal(t, 100, adjustment.RawUsage.InputTokens)
						require.Equal(t, 4, adjustment.RawUsage.CacheCreation1hTokens)
						require.Equal(t, gross.ActualCost, adjustment.CustomerGrossCost)
						require.InDelta(t, row.ActualCost, adjustment.CustomerGrossCost-adjustment.CustomerCredit, 1e-12)
						*result.Usage.RawPromptTokens = 999
						require.Equal(t, 130, *adjustment.RawUsage.RawPromptTokens)
					} else {
						require.Empty(t, row.MeteringAdjustment)
					}
				})
			}
		}
	}
}

func TestMeteringKnownUnknownAndFree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gross  *CostBreakdown
		card   *ChannelModelPricing
		source string
		want   *float64
	}{
		{"unknown", &CostBreakdown{}, nil, "unknown", nil},
		{"fallback", &CostBreakdown{TotalCost: 2}, nil, "fallback", new(1.0)},
		{"free", nil, &ChannelModelPricing{InputPrice: new(0.0)}, "contract", new(0.0)},
		{"missing_output_is_not_free", nil, &ChannelModelPricing{CacheReadPrice: new(0.0)}, "unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := MeteredUsage{InputTokens: 10, InputPresent: true}
			cost := supplierCostFromMetering(raw.tokens(), 1, knownMeteredTotal(tc.gross), tc.card, false, nil, "", 0.5)
			require.Equal(t, tc.source, cost.Source)
			require.Equal(t, tc.want, cost.Amount)
			row := &UsageLog{}
			applyMeteringAdjustment(row, raw, tc.gross, cost, nil)
			if tc.want != nil {
				require.Empty(t, row.MeteringAdjustment)
			} else {
				require.Contains(t, string(row.MeteringAdjustment), `"supplier_cost_source":"unknown"`)
			}
		})
	}
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "openai-metering", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 20, OutputTokens: 10}}, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3, RateMultiplier: new(0.7)}}))
	require.NotNil(t, logs.lastLog.SupplierCostEstimate)
	require.Equal(t, logs.lastLog.TotalCost*0.7, *logs.lastLog.SupplierCostEstimate)
	require.Empty(t, logs.lastLog.MeteringAdjustment)
	for _, free := range []bool{false, true} {
		channel := &Channel{ID: 1, Status: StatusActive}
		if free {
			channel.AccountStatsPricingRules = []AccountStatsPricingRule{{AccountIDs: []int64{3}, Pricing: []ChannelModelPricing{{Models: []string{"unpriced-fixture"}, InputPrice: new(0.0)}}}}
		}
		svc.channelService = newTestChannelServiceForStats(t, channel, 10, PlatformOpenAI)
		require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "unpriced-metering", Model: "unpriced-fixture", Usage: OpenAIUsage{InputTokens: 10}, ResponseFactsKnown: true, UsagePresent: true}, APIKey: &APIKey{ID: 2, GroupID: new(int64(10))}, User: &User{ID: 1}, Account: &Account{ID: 3}}))
		if free {
			require.Equal(t, new(0.0), logs.lastLog.SupplierCostEstimate)
			require.Empty(t, logs.lastLog.MeteringAdjustment)
		} else {
			require.Nil(t, logs.lastLog.SupplierCostEstimate)
			require.Contains(t, string(logs.lastLog.MeteringAdjustment), `"supplier_cost_source":"unknown"`)
		}
	}
}

func TestMeteringSnapshotAndLegacyIntent(t *testing.T) {
	var old UsageBillingCommand
	require.NoError(t, json.Unmarshal([]byte(`{"RequestID":"old","UserID":1,"APIKeyID":2,"AccountID":3,"BalanceCost":1.25,"Usage":{"RequestID":"old","UserID":1,"APIKeyID":2,"AccountID":3,"ActualCost":1.25,"TotalCost":1.25}}`), &old))
	old.Normalize()
	require.Nil(t, old.Usage.SupplierCostEstimate)
	require.Empty(t, old.Usage.MeteringAdjustment)
	fingerprint := old.RequestFingerprint
	old.Usage.SupplierCostEstimate = new(0.9)
	old.Usage.MeteringAdjustment = json.RawMessage(`{"v":1,"supplier_cost_source":"contract"}`)
	snapshot := BillingUsageSnapshot(old.Usage)
	old.Usage.MeteringAdjustment[0] = '!'
	*old.Usage.SupplierCostEstimate = 8
	require.Equal(t, 0.9, *snapshot.SupplierCostEstimate)
	require.True(t, json.Valid(snapshot.MeteringAdjustment))
	old.Usage = snapshot
	old.RequestFingerprint = ""
	old.Normalize()
	require.Equal(t, fingerprint, old.RequestFingerprint)
	encoded, err := json.Marshal(old)
	require.NoError(t, err)
	var restored UsageBillingCommand
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.NoError(t, restored.Validate())
	require.Equal(t, snapshot.SupplierCostEstimate, restored.Usage.SupplierCostEstimate)
}

func TestMeteringAbuseKeepsLegacyCharge(t *testing.T) {
	withResilienceSettings(t, nil)
	r := NewResilienceService(nil, newMemResilienceStore())
	r.ConfigureUndelivered(nil)
	t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
	var ctx context.Context
	for i := 0; i < 31; i++ {
		ctx = disconnectedState(t, 0, 99)
	}
	for _, explicit := range []bool{false, true} {
		row := &UsageLog{TotalCost: 2, ActualCost: 3, SupplierCostEstimate: new(100.0)}
		want := 1.0
		if explicit {
			row.AccountStatsCost = new(0.75)
			want = 0.75
		}
		cost := &CostBreakdown{TotalCost: 2, ActualCost: 3}
		require.False(t, resilienceUndeliveredUsage(ctx, row, cost, 0.5))
		require.Equal(t, want, row.ActualCost)
		require.Equal(t, want, cost.ActualCost)
	}
}

func BenchmarkMeteringAdjustment(b *testing.B) {
	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	result := &ForwardResult{Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 10}}
	raw := freezeMeteredUsage(result.Usage, true, true, "upstream")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		gross := svc.calculateRecordUsageCost(context.Background(), result, &APIKey{}, result.Model, 1, 1, at)
		row := &UsageLog{ActualCost: 0.0001}
		estimate := supplierCostFromMetering(raw.tokens(), 1, knownMeteredTotal(gross), nil, false, nil, "", 1)
		applyMeteringAdjustment(row, raw, gross, estimate, []meteringAdjustmentKind{{Kind: "force_cache_billing", InputToCacheTokens: 100}})
		b.ReportMetric(float64(len(row.MeteringAdjustment)), "json_bytes")
	}
}
