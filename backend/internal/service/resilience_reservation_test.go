//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func newResilientEstimateGateway(t *testing.T) (*GatewayService, *ModelPricing) {
	t.Helper()
	withResilienceSettings(t, nil)
	svc := newInflightEstimateGateway(t, nil)
	svc.resilience = NewResilienceService(nil, newMemResilienceStore())
	pricing, err := svc.billingService.GetModelPricing("claude-sonnet-4-5")
	require.NoError(t, err)
	require.Positive(t, pricing.CacheReadPricePerToken)
	return svc, pricing
}

func TestCacheAwareReservationEstimate(t *testing.T) {
	// Spec: a session whose last request read 90 % of its input from cache
	// prices about 90 % of the next input at the cache-read price.
	svc, p := newResilientEstimateGateway(t)
	ctx := context.Background()
	apiKey := &APIKey{User: &User{ID: 3}}
	req := InflightEstimateRequest{Model: "claude-sonnet-4-5", BodyBytes: 40000, MaxTokens: 1000, SessionKey: "metadata.user_id=s1"}
	full := 10000*p.InputPricePerToken + 1000*p.OutputPricePerToken

	est, ok := svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, ok)
	require.InDelta(t, full, est, 1e-12, "no history: unchanged")

	// The billing task of the previous request records its share.
	res := &InflightReservation{userID: 3}
	res.SetCacheSession(req.SessionKey)
	svc.resilience.NoteSessionCacheUsage(WithInflightReservation(ctx, res), 9000, 10000)

	est, _ = svc.EstimateInflightReservation(ctx, apiKey, req)
	cached := 10000*(0.1*p.InputPricePerToken+0.9*p.CacheReadPricePerToken) + 1000*p.OutputPricePerToken
	require.InDelta(t, cached, est, 1e-12)

	// Other sessions and other users with the same identifier are unaffected.
	other := req
	other.SessionKey = "metadata.user_id=s2"
	est, _ = svc.EstimateInflightReservation(ctx, apiKey, other)
	require.InDelta(t, full, est, 1e-12)
	est, _ = svc.EstimateInflightReservation(ctx, &APIKey{User: &User{ID: 4}}, req)
	require.InDelta(t, full, est, 1e-12)

	// Switch off: original estimate.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Reservation.CacheAwareEstimate = false })
	est, _ = svc.EstimateInflightReservation(ctx, apiKey, req)
	require.InDelta(t, full, est, 1e-12)
}

func TestCacheAwareEstimateSkipsUntrackedRequests(t *testing.T) {
	svc, _ := newResilientEstimateGateway(t)
	store := svc.resilience.store.(*memResilienceStore)

	// Without a reservation or session nothing is recorded.
	svc.resilience.NoteSessionCacheUsage(context.Background(), 9000, 10000)
	svc.resilience.NoteSessionCacheUsage(WithInflightReservation(context.Background(), &InflightReservation{userID: 3}), 9000, 10000)
	require.Empty(t, store.ratios)

	res := &InflightReservation{userID: 3}
	res.SetCacheSession("prompt_cache_key=abc")
	svc.resilience.NoteSessionCacheUsage(WithInflightReservation(context.Background(), res), 0, 0)
	require.Empty(t, store.ratios, "empty usage is not a signal")
	svc.resilience.NoteSessionCacheUsage(WithInflightReservation(context.Background(), res), 0, 5000)
	require.Len(t, store.ratios, 1, "a cold request resets the share")
}

func TestOutputP99CapsReservationOutput(t *testing.T) {
	svc, p := newResilientEstimateGateway(t)
	ctx := context.Background()
	apiKey := &APIKey{User: &User{ID: 3}}
	repo := &fakeResilienceRepo{p99: map[string]int{"Claude-Sonnet-4-5": 300, "claude-opus-4-1": 5000}}
	svc.resilience.refreshOutputP99(ctx, repo)

	estimate := func(maxTokens int) float64 {
		est, ok := svc.EstimateInflightReservation(ctx, apiKey, InflightEstimateRequest{Model: "claude-sonnet-4-5", MaxTokens: maxTokens})
		require.True(t, ok)
		return est
	}
	require.InDelta(t, 1024*p.OutputPricePerToken, estimate(32000), 1e-12, "P99 below the floor: 1024")
	require.InDelta(t, 500*p.OutputPricePerToken, estimate(500), 1e-12, "never above max_tokens")

	// A failed refresh keeps the last caps.
	repo.p99Err = errors.New("db down")
	svc.resilience.refreshOutputP99(ctx, repo)
	require.InDelta(t, 1024*p.OutputPricePerToken, estimate(32000), 1e-12)
	require.Equal(t, 5000, svc.resilience.outputP99Cap("claude-opus-4-1"))
	require.Zero(t, svc.resilience.outputP99Cap("unknown-model"))

	withResilienceSettings(t, func(s *resilience.Settings) { s.Reservation.OutputP99Cap = false })
	require.InDelta(t, 32000*p.OutputPricePerToken, estimate(32000), 1e-12, "switch off: max_tokens")
}

func TestForcedCacheBillingKeepsTheRealCacheShare(t *testing.T) {
	// A sticky session that switched accounts is billed as if its input were
	// cache reads; the next request's reservation must still see the real
	// share (here: nothing came from cache).
	withResilienceSettings(t, nil)
	store := newMemResilienceStore()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.resilience = NewResilienceService(nil, store)
	res := &InflightReservation{userID: 601}
	res.SetCacheSession("metadata.user_id=s1")
	err := svc.RecordUsage(WithInflightReservation(context.Background(), res), &RecordUsageInput{
		Result:            &ForwardResult{RequestID: "force-cache", Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 5}, Model: "claude-sonnet-4", Duration: time.Second},
		APIKey:            &APIKey{ID: 501, Quota: 100},
		User:              &User{ID: 601},
		Account:           &Account{ID: 701},
		ForceCacheBilling: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1000, usageRepo.lastLog.CacheReadTokens, "billed as cache reads")
	ratio, ok, err := store.SessionCacheRatio(context.Background(), sessionCacheRatioKey(601, "metadata.user_id=s1"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, ratio)
}
