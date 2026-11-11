package service

import (
	"math"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// openAIProfitGateCost is the single real-time cost source used by the profit
// gate, quality usability checks, fallback decisions and preview. The raw
// stored multiplier is validated first: a fresh API-key probe may replace it,
// while OAuth-like accounts use the scheduler's reference multiplier.
func openAIProfitGateCost(acc *Account, at time.Time) (float64, bool) {
	if acc == nil || acc.RateMultiplier == nil {
		return 0, false
	}
	raw := *acc.RateMultiplier
	if raw < 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, false
	}
	var oauthRate *float64
	if acc.IsOpenAIOAuthLike() {
		oauthRate = cachedOpenAIOAuthSchedulingRateMultiplier()
	} else if !acc.IsOpenAIApiKey() {
		// The shared profit gate also covers the other token platforms, which
		// have no OpenAI scheduling-rate signal; their validated multiplier is
		// already the live cost source.
		return raw, true
	}
	cost, ok := openAISchedulingRate(acc, at, oauthRate)
	if !ok || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0, false
	}
	return cost, true
}

// groupProfitCeiling derives the default-account ceiling for one group.
func groupProfitCeiling(group *Group, at time.Time) (float64, bool) {
	if group == nil || !group.ProfitControlEnabled {
		return 0, false
	}
	deduction := group.ProfitMinMargin + group.ProfitSafetyBuffer
	if deduction < 0 || deduction >= 1 || math.IsNaN(deduction) || math.IsInf(deduction, 0) {
		return 0, true
	}
	return clampProfitControlThreshold(group.RateMultiplier * group.PeakMultiplierAt(at) * (1 - deduction)), true
}

// groupMemberUsable contains the hard eligibility rules shared by quality
// guard, fallback and operations. Soft resilience penalties are intentionally
// left to groupMemberServable.
func groupMemberUsable(group *Group, acc *Account, now time.Time) bool {
	if group == nil || acc == nil {
		return false
	}
	platform := group.Platform
	if platform == PlatformComposite {
		if !isConcreteRequestPlatform(acc.Platform) {
			return false
		}
		platform = acc.Platform
	}
	if isPlatformFilteredForSelection(acc, platform, platform == PlatformAnthropic || platform == PlatformGemini) {
		return false
	}
	tier, _, _ := resiliencePenaltyOf(acc, "", now)
	if tier == resilience.TierHard {
		return false
	}
	if ceiling, on := groupProfitCeiling(group, now); on {
		cost, ok := openAIProfitGateCost(acc, now)
		if !ok || profitControlOverThreshold(cost, ceiling) {
			return false
		}
	}
	return true
}

// groupMemberServable is the shared runtime availability decision. Quality
// tiers affect ordering and migration, never member availability.
// A nil penalty function uses the default resiliencePenaltyOf classifier; it
// never skips soft-penalty checks.
func groupMemberServable(group *Group, acc *Account, now time.Time, penalty func(*Account, time.Time) resilience.PenaltyTier) bool {
	if !groupMemberUsable(group, acc, now) {
		return false
	}
	if penalty == nil {
		penalty = func(a *Account, at time.Time) resilience.PenaltyTier {
			tier, _, _ := resiliencePenaltyOf(a, "", at)
			return tier
		}
	}
	if penalty(acc, now) != resilience.TierNone {
		return false
	}
	return true
}
