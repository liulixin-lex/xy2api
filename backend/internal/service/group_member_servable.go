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
	rate, _, ok := openAIProfitGateCostSource(acc, at)
	return rate, ok
}

func openAIProfitGateCostSource(acc *Account, at time.Time) (float64, string, bool) {
	if acc == nil || acc.RateMultiplier == nil {
		return 0, "stored", false
	}
	raw := *acc.RateMultiplier
	if raw < 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, "stored", false
	}
	if acc.IsOpenAIOAuthLike() {
		if rate := cachedOpenAIOAuthSchedulingRateMultiplier(); rate != nil && *rate >= 0 && !math.IsNaN(*rate) && !math.IsInf(*rate, 0) {
			return *rate, "oauth_setting", true
		}
	} else if acc.IsOpenAIApiKey() {
		if rate, ok := openAIFreshUpstreamBillingRate(acc, at); ok && rate >= 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0) {
			return rate, "upstream_probe", true
		}
	}
	return raw, "stored", true
}

// profitRangeVerdict is shared by live admission, fallback and both previews.
func profitRangeVerdict(rate float64, ok bool, ceiling *float64, floor float64, pinned bool) string {
	if pinned {
		return "pinned"
	}
	if !ok || rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return openAIProfitFilterReasonInvalidAccountRate
	}
	if floor > 0 && floor-rate > profitControlRateEpsilon*math.Max(1, floor) {
		return openAIProfitFilterReasonFloor
	}
	if ceiling != nil && profitControlOverThreshold(rate, *ceiling) {
		return openAIProfitFilterReasonThreshold
	}
	return "pass"
}

func profitVerdictAdmitted(verdict string) bool { return verdict == "pass" || verdict == "pinned" }

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
	ceiling, ceilingOn := groupProfitCeiling(group, now)
	floor := groupProfitFloor(group)
	if ceilingOn || floor > 0 {
		var limit *float64
		if ceilingOn {
			limit = &ceiling
		}
		cost, ok := openAIProfitGateCost(acc, now)
		return profitVerdictAdmitted(profitRangeVerdict(cost, ok, limit, floor, group.Platform == PlatformOpenAI && acc.PinnedIn(group.ID)))
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
