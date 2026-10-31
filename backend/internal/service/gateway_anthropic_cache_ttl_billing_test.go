//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropicCacheTTLReservationAliasPriceUpperBound(t *testing.T) {
	for _, target := range []string{"claude-sonnet-4-5", "claude-fable-5-1", "claude-mythos-preview", "kimi-k3"} {
		for _, declared := range []bool{false, true} {
			t.Run(target+map[bool]string{false: "/fallback", true: "/declared"}[declared], func(t *testing.T) {
				fixture, key, _ := cacheFixture(t)
				key.AnthropicCacheTTLMode = "1h"
				svc := newInflightEstimateGateway(t, nil)
				svc.settingService = fixture.settingService
				input, output, hour := 10e-6, 0.0, 30e-6
				key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"my-model"}, BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output, CacheWrite1hPrice: &hour}}
				snap := &inflightSnapshotCacheStub{byBucket: map[string][]Account{inflightBucketKey(*key.GroupID, PlatformAnthropic): {{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"my-model": target}}}}}}
				attachInflightSnapshot(svc, snap)
				raw := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
				if declared {
					raw = []byte(`{"cache_control":{"type":"ephemeral","ttl":"1h"}}`)
				}
				ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, raw)
				got, ok := svc.EstimateInflightReservation(ctx, key, InflightEstimateRequest{Model: "my-model", BodyBytes: 4000, MaxTokens: 1})
				require.True(t, ok)
				want := 1000 * input
				if isActualClaudeModel(target) {
					want = 1000 * hour
				}
				require.InDelta(t, want, got, 1e-12)
			})
		}
	}
}

func TestAnthropicCacheTTLReservationNoInjectionAndHistoricalRatio(t *testing.T) {
	fixture, key, _ := cacheFixture(t)
	key.AnthropicCacheTTLMode = "1h"
	fixture.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Minute)})
	svc := newInflightEstimateGateway(t, nil)
	svc.settingService = fixture.settingService
	input, output, read, hour := 10e-6, 0.0, 1e-6, 30e-6
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{cacheTestModel}, BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output, CacheReadPrice: &read, CacheWrite1hPrice: &hour}}
	snap := &inflightSnapshotCacheStub{byBucket: map[string][]Account{inflightBucketKey(*key.GroupID, PlatformAnthropic): {{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}}}}
	attachInflightSnapshot(svc, snap)
	deps := svc.inflightEstimateDeps()
	deps.cacheRatio = func(context.Context, int64, string) (float64, bool) { return .9, true }
	// The real admission with API-key-only candidates has no possible injection.
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(`{"messages":[]}`))
	req := InflightEstimateRequest{Model: cacheTestModel, BodyBytes: 4000, MaxTokens: 1}
	got, ok := svc.EstimateInflightReservation(ctx, key, req)
	require.True(t, ok)
	require.InDelta(t, 1000*input, got, 1e-12)
	req.ClaudeCacheColdWriteTTL = "1h"
	cold, ok := deps.estimate(ctx, key, req)
	require.True(t, ok)
	require.InDelta(t, 1000*hour, cold, 1e-12, "cold writes must ignore historical cache-read ratio")
	req.ClaudeCacheColdWriteTTL = ""
	warm, ok := deps.estimate(ctx, key, req)
	require.True(t, ok)
	require.InDelta(t, 1000*(.1*input+.9*read), warm, 1e-12)
}

func TestAnthropicCacheTTLSettlementCopy(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	users := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{})
	original := ClaudeUsage{InputTokens: 100, OutputTokens: 5, CacheReadInputTokens: 40, CacheCreationInputTokens: 100, CacheCreationInputTokensPresent: true, CacheCreation5mTokens: 30, CacheCreation5mTokensPresent: true, CacheCreation1hTokens: 20, CacheCreation1hTokensPresent: true}
	result := &ForwardResult{RequestID: "ttl-settlement-copy", Model: cacheTestModel, Usage: original}
	policy, applied := "1h", true
	result.AnthropicCacheTTLPolicy = &policy
	result.AnthropicCacheTTLApplied = &applied
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 1}, User: &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: map[string]any{"cache_ttl_override_enabled": true, "cache_ttl_override_target": "5m"}}, ForceCacheBilling: true})
	require.NoError(t, err)
	require.Equal(t, original, result.Usage)
	require.Equal(t, 0, logs.lastLog.InputTokens)
	require.Equal(t, 140, logs.lastLog.CacheReadTokens)
	require.Equal(t, 100, logs.lastLog.CacheCreation5mTokens)
	require.Zero(t, logs.lastLog.CacheCreation1hTokens)
	require.True(t, logs.lastLog.CacheTTLOverridden)
	require.Equal(t, &policy, logs.lastLog.AnthropicCacheTTLPolicy)
	require.Equal(t, &applied, logs.lastLog.AnthropicCacheTTLApplied)
}

func TestAnthropicCacheTTLReservationChannelOAuthInjection(t *testing.T) {
	for _, target := range []string{"claude-sonnet-4-5", "claude-fable-5-1", "kimi-k3"} {
		t.Run(target, func(t *testing.T) {
			fixture, key, _ := cacheFixture(t)
			key.AnthropicCacheTTLMode = "1h"
			fixture.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Minute)})
			ch := Channel{ID: 3, Status: StatusActive, GroupIDs: []int64{*key.GroupID}, BillingModelSource: BillingModelSourceRequested, ModelMapping: map[string]map[string]string{PlatformAnthropic: {"channel-alias": target}}}
			channel := newTestChannelService(makeStandardRepo(ch, map[int64]string{*key.GroupID: PlatformAnthropic}))
			svc := newInflightEstimateGateway(t, channel)
			svc.settingService = fixture.settingService
			input, output, hour := 10e-6, 0.0, 30e-6
			key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"channel-alias"}, BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output, CacheWrite1hPrice: &hour}}
			snap := &inflightSnapshotCacheStub{byBucket: map[string][]Account{inflightBucketKey(*key.GroupID, PlatformAnthropic): {{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}}}}
			attachInflightSnapshot(svc, snap)
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
			got, ok := svc.EstimateInflightReservation(ctx, key, InflightEstimateRequest{Model: "channel-alias", BodyBytes: 4000, MaxTokens: 1})
			require.True(t, ok)
			want := 1000 * input
			if isActualClaudeModel(target) {
				want = 1000 * hour
			}
			require.InDelta(t, want, got, 1e-12)
		})
	}
}

func TestAnthropicCacheTTLReservationOAuthIgnoresAccountMapping(t *testing.T) {
	fixture, key, _ := cacheFixture(t)
	key.AnthropicCacheTTLMode = "1h"
	fixture.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Minute)})
	svc := newInflightEstimateGateway(t, nil)
	svc.settingService = fixture.settingService
	input, output, hour := 10e-6, 0.0, 30e-6
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"claude-sonnet-4-5"}, BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output, CacheWrite1hPrice: &hour}}
	snap := &inflightSnapshotCacheStub{byBucket: map[string][]Account{inflightBucketKey(*key.GroupID, PlatformAnthropic): {{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-5": "kimi-k3"}}}}}}
	attachInflightSnapshot(svc, snap)
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	got, ok := svc.EstimateInflightReservation(ctx, key, InflightEstimateRequest{Model: "claude-sonnet-4-5", BodyBytes: 4000, MaxTokens: 1})
	require.True(t, ok)
	require.InDelta(t, 1000*hour, got, 1e-12)
}
