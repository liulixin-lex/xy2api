//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeCacheColdWriteReservationCustomPrice(t *testing.T) {
	fixture, key, _ := cacheFixture(t)
	svc := newInflightEstimateGateway(t, nil)
	svc.settingService = fixture.settingService
	input, output, write := 4e-6, 20e-6, 9e-6
	key.Group.RateMultiplier = 1.2
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{cacheTestModel}, BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output, CacheWritePrice: &write}}
	req := InflightEstimateRequest{Model: cacheTestModel, BodyBytes: 4000, MaxTokens: 100}
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(`{"messages":[]}`))
	got, ok := svc.EstimateInflightReservation(ctx, key, req)
	require.True(t, ok)
	require.InDelta(t, (1000*write+100*output)*1.2, got, 1e-12)
	declared := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(`{"cache_control":null}`))
	normal, ok := svc.EstimateInflightReservation(declared, key, req)
	require.True(t, ok)
	require.InDelta(t, (1000*input+100*output)*1.2, normal, 1e-12)
	require.Greater(t, got, normal)
	write = 1e-6
	lower, ok := svc.EstimateInflightReservation(ctx, key, req)
	require.True(t, ok)
	require.InDelta(t, normal, lower, 1e-12, "never reserve less than normal input")
	key.Group.RateMultiplier = 0
	free, ok := svc.EstimateInflightReservation(ctx, key, req)
	require.True(t, ok)
	require.Zero(t, free)
}

func TestClaudeCacheActualUsageIndependentCost(t *testing.T) {
	svc := &BillingService{fallbackPrices: map[string]*ModelPricing{cacheTestModel: {InputPricePerToken: 4e-6, OutputPricePerToken: 20e-6, CacheReadPricePerToken: .2e-6, SupportsCacheBreakdown: true, CacheCreation5mPrice: 5e-6, CacheCreation1hPrice: 8e-6}}}
	for _, tc := range []struct {
		name              string
		total, five, hour int
		want              float64
	}{
		{"five", 1000, 1000, 0, .005}, {"hour", 1000, 0, 1000, .008}, {"mixed", 1000, 600, 400, .0062}, {"missing_detail", 1000, 0, 0, .005}, {"contradictory", 1000, 1000, 1000, .0065},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost, e := svc.CalculateCost(cacheTestModel, UsageTokens{InputTokens: 2, OutputTokens: 5, CacheCreationTokens: tc.total, CacheCreation5mTokens: tc.five, CacheCreation1hTokens: tc.hour, CacheReadTokens: 9000}, 1.2)
			require.NoError(t, e)
			require.InDelta(t, tc.want, cost.CacheCreationCost, 1e-12)
			require.InDelta(t, (2*4e-6+5*20e-6+9000*.2e-6+tc.want)*1.2, cost.ActualCost, 1e-12)
		})
	}
}

func TestClaudeCacheReservationIncludesAccountMappedModel(t *testing.T) {
	fixture, key, _ := cacheFixture(t)
	svc := newInflightEstimateGateway(t, nil)
	svc.settingService = fixture.settingService
	snap := &inflightSnapshotCacheStub{byBucket: map[string][]Account{inflightBucketKey(*key.GroupID, PlatformAnthropic): {{ID: 11, Platform: PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-5": "claude-opus-4-1"}}}}}}
	repo := attachInflightSnapshot(svc, snap)
	req := InflightEstimateRequest{Model: "claude-sonnet-4-5", BodyBytes: 4000, MaxTokens: 100}
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(`{"messages":[]}`))
	reserved, priced := svc.EstimateInflightReservation(ctx, key, req)
	require.True(t, priced)
	req.Model = "claude-opus-4-1"
	direct, priced := svc.EstimateInflightReservation(ctx, key, req)
	require.True(t, priced)
	require.InDelta(t, direct, reserved, 1e-12)
	require.Greater(t, snap.reads.Load(), int64(0))
	require.Zero(t, repo.dbCalls.Load())
}
