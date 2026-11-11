package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAISchedulingRateFallback(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name   string
		status string
		age    time.Duration
		probe  float64
		want   float64
	}{
		{"fresh snapshot wins", UpstreamBillingProbeStatusOK, time.Minute, 0.6, 0.6},
		{"fresh snapshot survives failed refresh", UpstreamBillingProbeStatusFailed, time.Minute, 0.6, 0.6},
		{"expired snapshot falls back", UpstreamBillingProbeStatusOK, 3 * time.Hour, 0.6, 0.3},
		{"future snapshot falls back", UpstreamBillingProbeStatusOK, -time.Hour, 0.6, 0.3},
		{"invalid snapshot falls back", UpstreamBillingProbeStatusOK, time.Minute, -1, 0.3},
		{"zero snapshot remains valid", UpstreamBillingProbeStatusOK, time.Minute, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account := upstreamCostTestAccount(1, tt.status, tt.probe, now.Add(-tt.age), time.Hour)
			account.RateMultiplier = floatPtr(0.3)
			rate, ok := openAISchedulingRate(account, now, floatPtr(0.9))
			require.True(t, ok)
			require.Equal(t, tt.want, rate)
		})
	}

	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType+" without probe or override", func(t *testing.T) {
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType}
			for _, tt := range []struct {
				configured *float64
				want       float64
				known      bool
			}{
				{floatPtr(0.3), 0.3, true},
				{floatPtr(0), 0, true},
				{nil, 1, true},
				{floatPtr(-1), 1, true},
				{floatPtr(math.NaN()), 0, false},
				{floatPtr(math.Inf(1)), 0, false},
			} {
				account.RateMultiplier = tt.configured
				rate, ok := openAISchedulingRate(account, now, nil)
				require.Equal(t, tt.known, ok)
				if ok {
					require.Equal(t, tt.want, rate)
				}
			}
		})
	}

	oauth := upstreamCostTestOAuthAccount(1)
	oauth.RateMultiplier = floatPtr(0.3)
	for _, override := range []float64{0, 0.7} {
		rate, ok := openAISchedulingRate(oauth, now, &override)
		require.True(t, ok)
		require.Equal(t, override, rate)
	}
	for _, override := range []float64{-1, math.NaN(), math.Inf(1)} {
		rate, ok := openAISchedulingRate(oauth, now, &override)
		require.True(t, ok)
		require.Equal(t, 0.3, rate)
	}
	_, ok := openAISchedulingRate(nil, now, nil)
	require.False(t, ok)
}

func TestOpenAISchedulingRateFallbackSelectionKeys(t *testing.T) {
	now := time.Now()
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			cheap := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, RateMultiplier: floatPtr(.3)}
			expensive := &Account{ID: 2, Platform: PlatformOpenAI, Type: accountType, RateMultiplier: floatPtr(.8)}
			req := selectionRequest{TokenBilled: true}
			a, b := buildSelectionKey(nil, 0, "m", cheap, req, now), buildSelectionKey(nil, 0, "m", expensive, req, now)
			require.True(t, selectionLess(a, b))
			require.Equal(t, .3, a.Rate)
			require.Equal(t, .8, b.Rate)
			if accountType == AccountTypeOAuth {
				req.OAuthRateMultiplier = floatPtr(.7)
				a, b = buildSelectionKey(nil, 0, "m", cheap, req, now), buildSelectionKey(nil, 0, "m", expensive, req, now)
				require.Equal(t, a.Rate, b.Rate)
				require.Equal(t, .7, a.Rate)
			}
		})
	}
}

func TestOpenAIOAuthSchedulingRateRuntimePreservesExplicitClear(t *testing.T) {
	resetOpenAISchedulingRateTestCache()
	defer resetOpenAISchedulingRateTestCache()
	for _, tt := range []struct {
		name    string
		values  map[string]string
		want    *float64
		startup bool
	}{
		{"absent", map[string]string{}, floatPtr(1), false},
		{"cleared", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: ""}, nil, false},
		{"zero", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: "0"}, floatPtr(0), false},
		{"override", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: "0.7"}, floatPtr(0.7), false},
		{"unloaded cache warms at startup without scheduling", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: "0.2", SettingKeyCodexCLIOnlyEngineFingerprintSignals: "[]"}, floatPtr(0.2), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetOpenAISchedulingRateTestCache()
			repo := &openAIAdvancedSchedulerSettingRepoStub{values: tt.values}
			if tt.startup {
				require.Equal(t, floatPtr(1), cachedOpenAIOAuthSchedulingRateMultiplier())
				ProvideSettingService(repo, nil, nil, nil, nil)
			}
			svc := &OpenAIGatewayService{
				rateLimitService: &RateLimitService{settingService: &SettingService{
					settingRepo: repo,
				}},
			}
			for i := 0; i < 2; i++ {
				if !tt.startup {
					require.Equal(t, tt.want, svc.openAIOAuthSchedulingRateMultiplier(context.Background()))
				}
				account := upstreamCostTestOAuthAccount(1)
				account.RateMultiplier = floatPtr(0.3)
				want := 0.3
				if tt.want != nil {
					want = *tt.want
				}
				cost, ok := openAIProfitGateCost(account, time.Now())
				require.True(t, ok)
				require.Equal(t, want, cost, "profit gate shares the scheduler reference cache")
			}
		})
	}
}

func resetOpenAISchedulingRateTestCache() {
	openAISchedulingRateCache.Store(nil)
	openAISchedulingRateSF.Forget(SettingKeyOpenAIOAuthSchedulingRateMultiplier)
}

type selectionRateOnlyRepo struct {
	SettingRepository
	keys []string
}

func (r *selectionRateOnlyRepo) GetValue(_ context.Context, key string) (string, error) {
	r.keys = append(r.keys, key)
	return "0.6", nil
}
func TestOpenAISchedulingRateReadsOnlyMultiplier(t *testing.T) {
	saved := openAISchedulingRateCache.Load()
	resetOpenAISchedulingRateTestCache()
	t.Cleanup(func() { resetOpenAISchedulingRateTestCache(); openAISchedulingRateCache.Store(saved) })
	repo := &selectionRateOnlyRepo{}
	svc := &OpenAIGatewayService{settingService: &SettingService{settingRepo: repo}}
	require.Equal(t, floatPtr(.6), svc.openAIOAuthSchedulingRateMultiplier(context.Background()))
	require.Equal(t, []string{SettingKeyOpenAIOAuthSchedulingRateMultiplier}, repo.keys)
	svc.settingService.refreshCachedSettings(&SystemSettings{OpenAIOAuthSchedulingRateMultiplier: floatPtr(.4)})
	require.Equal(t, floatPtr(.4), svc.openAIOAuthSchedulingRateMultiplier(context.Background()))
	require.Len(t, repo.keys, 1, "settings refresh publishes the independent cache")
}
