package service

import (
	"math"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// openAIProfitGateCost is the WP1 cost source. WP3 replaces the value source
// with the fresh scheduling probe while preserving this validation contract.
func openAIProfitGateCost(acc *Account, _ time.Time) (float64, bool) {
	if acc == nil || acc.RateMultiplier == nil {
		return 0, false
	}
	cost := *acc.RateMultiplier
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
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
	return group.RateMultiplier * group.PeakMultiplierAt(at) * (1 - deduction), true
}

// groupMemberUsable contains the hard eligibility rules shared by quality
// guard, fallback and operations. Soft resilience penalties are intentionally
// left to groupMemberServable.
func groupMemberUsable(group *Group, acc *Account, now time.Time) bool {
	if group == nil || acc == nil || acc.Platform != group.Platform || !acc.IsActive() || !acc.Schedulable {
		return false
	}
	if acc.AutoPauseOnExpired && acc.ExpiresAt != nil && !now.Before(*acc.ExpiresAt) {
		return false
	}
	if acc.Status == StatusError || acc.IsAPIKeyOrBedrock() && acc.IsQuotaExceeded() {
		return false
	}
	tier, _, _ := resiliencePenaltyOf(acc, "", now)
	if tier == resilience.TierHard {
		return false
	}
	if ceiling, on := groupProfitCeiling(group, now); on {
		cost, ok := openAIProfitGateCost(acc, now)
		if !ok || cost > ceiling {
			return false
		}
	}
	return true
}

// groupMemberServable is the single soft-state/quality decision used by WP2/WP3.
// A nil penalty function uses the default resiliencePenaltyOf classifier; it
// never skips soft-penalty checks.
func groupMemberServable(group *Group, acc *Account, now time.Time, view GroupQualityGateView, penalty func(*Account, time.Time) resilience.PenaltyTier) bool {
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
	if view != nil && view.Vetoed(group.ID, acc.ID) {
		return false
	}
	return true
}
