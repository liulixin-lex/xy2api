//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNativeAnthropicTTLProjection(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		aggregate, five, hour                      int
		aggregatePresent, fivePresent, hourPresent bool
		want                                       float64
	}{
		{"hour", 1000, 0, 1000, true, true, true, 0.01},
		{"explicit_zero_aggregate", 0, 0, 1000, true, false, true, 0},
		{"missing_aggregate", 1000, 0, 1000, false, false, true, 0.01},
		{"partial_buckets", 1000, 0, 500, true, false, true, 0.0075},
		{"missing_buckets", 1000, 0, 0, true, false, false, 0.005},
		{"explicit_zero_buckets", 1000, 0, 0, true, true, true, 0.005},
	} {
		for _, origin := range []string{"catalog", "group", "channel"} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, origin), func(t *testing.T) {
				repo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.cfg.Default.RateMultiplier = 1
				catalog := newStubPricingServiceFromJSON(t, `{"fixture-ttl":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"cache_creation_input_token_cost":0.000005,"cache_creation_input_token_cost_above_1hr":0.00001}}`)
				billing := NewBillingService(&config.Config{}, catalog)
				svc.billingService = billing
				svc.resolver = NewModelPricingResolver(nil, billing)
				group := &Group{ID: 100, Platform: PlatformAnthropic, RateMultiplier: 1}
				want := tc.want
				if origin != "catalog" {
					card := ChannelModelPricing{Platform: PlatformAnthropic, Models: []string{"fixture-ttl"}, CacheWritePrice: new(float64(6e-6)), CacheWrite1hPrice: new(float64(2e-5))}
					if origin == "group" {
						group.ModelPricing = []ChannelModelPricing{card}
					} else {
						billing, svc.resolver = newTokenCostTestEnv(t, PlatformAnthropic, []ChannelModelPricing{card}, catalog)
						svc.billingService = billing
					}
					switch tc.name {
					case "hour", "missing_aggregate":
						want = 0.02
					case "partial_buckets":
						want = 0.013
					case "explicit_zero_aggregate":
						want = 0
					default:
						want = 0.006
					}
				}
				key := &APIKey{ID: 2, GroupID: &group.ID, Group: group}
				result := &OpenAIForwardResult{RequestID: "ttl-fixture", Model: "fixture-ttl", Usage: OpenAIUsage{InputTokens: tc.aggregate, CacheCreationInputTokens: tc.aggregate, CacheCreationInputTokensPresent: tc.aggregatePresent, CacheCreation5mTokens: tc.five, CacheCreation1hTokens: tc.hour, CacheCreation5mTokensPresent: tc.fivePresent, CacheCreation1hTokensPresent: tc.hourPresent}}
				require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: &Account{ID: 3}}))
				require.NotNil(t, repo.lastLog)
				require.InDelta(t, want, repo.lastLog.CacheCreationCost, 1e-12)
				require.Equal(t, tc.five, repo.lastLog.CacheCreation5mTokens)
				require.Equal(t, tc.hour, repo.lastLog.CacheCreation1hTokens)
				generic := &GatewayService{billingService: billing, resolver: svc.resolver}
				comparison := generic.calculateTokenCost(context.Background(), &ForwardResult{Model: "fixture-ttl", Usage: ClaudeUsage{CacheCreationInputTokens: tc.aggregate, CacheCreationInputTokensPresent: tc.aggregatePresent, CacheCreation5mTokens: tc.five, CacheCreation1hTokens: tc.hour, CacheCreation5mTokensPresent: tc.fivePresent, CacheCreation1hTokensPresent: tc.hourPresent}}, key, "fixture-ttl", 1, time.Now())
				require.InDelta(t, comparison.CacheCreationCost, repo.lastLog.CacheCreationCost, 1e-12)
			})
		}
	}
}
