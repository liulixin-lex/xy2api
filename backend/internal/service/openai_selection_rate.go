package service

import (
	"context"
	"math"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type cachedOpenAISchedulingRate struct {
	rate      *float64
	expiresAt time.Time
}

var openAISchedulingRateCache atomic.Pointer[cachedOpenAISchedulingRate]
var openAISchedulingRateSF singleflight.Group

func publishOpenAISchedulingRate(rate *float64) {
	if rate != nil {
		value := *rate
		rate = &value
	}
	openAISchedulingRateSF.Forget(SettingKeyOpenAIOAuthSchedulingRateMultiplier)
	openAISchedulingRateCache.Store(&cachedOpenAISchedulingRate{rate: rate, expiresAt: time.Now().Add(openAISchedulingRateCacheTTL)})
}

func cachedOpenAIOAuthSchedulingRateMultiplier() *float64 {
	if cached := openAISchedulingRateCache.Load(); cached != nil {
		return cached.rate
	}
	return parseOpenAIOAuthSchedulingRateMultiplier(nil)
}

func loadOpenAISchedulingRate(ctx context.Context, repo SettingRepository) *float64 {
	if cached := openAISchedulingRateCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.rate
	}
	_, _, _ = openAISchedulingRateSF.Do(SettingKeyOpenAIOAuthSchedulingRateMultiplier, func() (any, error) {
		if cached := openAISchedulingRateCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
			return nil, nil
		}
		rate := parseOpenAIOAuthSchedulingRateMultiplier(nil)
		if repo != nil {
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAISchedulingRateDBTimeout)
			defer cancel()
			if raw, err := repo.GetValue(dbCtx, SettingKeyOpenAIOAuthSchedulingRateMultiplier); err == nil {
				rate = parseOpenAIOAuthSchedulingRateMultiplier(map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: raw})
			}
		}
		openAISchedulingRateCache.Store(&cachedOpenAISchedulingRate{rate: rate, expiresAt: time.Now().Add(openAISchedulingRateCacheTTL)})
		return nil, nil
	})
	return cachedOpenAIOAuthSchedulingRateMultiplier()
}

func (s *OpenAIGatewayService) openAIOAuthSchedulingRateMultiplier(ctx context.Context) *float64 {
	var repo SettingRepository
	if s != nil && s.settingService != nil {
		repo = s.settingService.settingRepo
	} else if s != nil && s.rateLimitService != nil && s.rateLimitService.settingService != nil {
		repo = s.rateLimitService.settingService.settingRepo
	}
	return loadOpenAISchedulingRate(ctx, repo)
}

func openAISchedulingRate(account *Account, now time.Time, oauthSchedulingRateMultiplier *float64) (float64, bool) {
	if account == nil || (!account.IsOpenAIApiKey() && !account.IsOpenAIOAuthLike()) {
		return 0, false
	}
	if account.IsOpenAIOAuthLike() {
		if rate := oauthSchedulingRateMultiplier; rate != nil && *rate >= 0 && !math.IsNaN(*rate) && !math.IsInf(*rate, 0) {
			return *rate, true
		}
	} else if rate, ok := openAIFreshUpstreamBillingRate(account, now); ok {
		return rate, true
	}
	rate := account.BillingRateMultiplier()
	return rate, !math.IsNaN(rate) && !math.IsInf(rate, 0)
}

func openAIFreshUpstreamBillingRate(account *Account, now time.Time) (float64, bool) {
	if !isUpstreamBillingProbeAccount(account) {
		return 0, false
	}
	snapshot := decodeUpstreamBillingProbeSnapshot(account.Extra)
	if snapshot == nil || (snapshot.Status != UpstreamBillingProbeStatusOK && snapshot.Status != UpstreamBillingProbeStatusFailed) ||
		snapshot.ReceivedAt == nil || snapshot.ReceivedAt.IsZero() {
		return 0, false
	}
	receivedAt := *snapshot.ReceivedAt
	freshUntil := snapshot.FreshUntil
	if freshUntil == nil && snapshot.Status == UpstreamBillingProbeStatusOK {
		interval := snapshot.NextProbeAt.Sub(receivedAt)
		if interval > 0 {
			freshUntil = probeTimePtr(receivedAt.Add(2 * interval))
		}
	}
	if freshUntil == nil || !freshUntil.After(receivedAt) || now.Before(receivedAt) || now.After(*freshUntil) {
		return 0, false
	}
	return upstreamBillingRateAt(snapshot.Data, now)
}

const (
	openAISchedulingRateCacheTTL  = 5 * time.Second
	openAISchedulingRateDBTimeout = 2 * time.Second
)
