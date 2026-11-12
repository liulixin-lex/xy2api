package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"time"
)

type gatewayCandidateReason string

// gatewayCandidateGates are the original Sub2API per-account gates of one
// gateway selection. Share candidates and last-resort selection both run
// accounts through them, so neither can miss a gate the other applies.
type gatewayCandidateGates struct {
	s            *GatewayService
	groupID      *int64
	platform     string
	useMixed     bool
	model        string
	channelCheck bool
}

func (s *GatewayService) newGatewayCandidateGates(ctx context.Context, groupID *int64, platform string, useMixed bool, model string) gatewayCandidateGates {
	return gatewayCandidateGates{s: s, groupID: groupID, platform: platform, useMixed: useMixed, model: model,
		channelCheck: s.needsUpstreamChannelRestrictionCheck(ctx, groupID)}
}

// candidateReason returns the first gate that rejects acc, or "" when all
// pass, with the expected recovery of a soft penalty (shareSoftRecovery).
// sticky grants the sticky allowance of the window cost and RPM limits. In a
// last-resort context the caller has cleared the soft account states, and a
// per-model rate limit within the soft threshold passes.
func (g gatewayCandidateGates) candidateReason(ctx context.Context, acc *Account, sticky bool, now time.Time) (gatewayCandidateReason, time.Time) {
	s := g.s
	switch {
	case !s.isAccountSchedulableForSelection(acc):
		reason, end := shareAccountStateReason(acc, now)
		return reason, shareSoftRecovery(acc, g.model, now, resiliencePenaltyOf, end)
	case !s.isGatewayAccountProfitEligible(ctx, acc):
		return "profit_gate", time.Time{}
	case !s.isAccountAllowedForPlatform(acc, g.platform, g.useMixed):
		return "platform_mismatch", time.Time{}
	case g.model != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, g.model):
		return "model_unsupported", time.Time{}
	case g.channelCheck && s.isUpstreamModelRestrictedByChannel(ctx, *g.groupID, acc, g.model):
		return "channel_restricted", time.Time{}
	case !s.isAccountSchedulableForModelSelection(ctx, acc, g.model) && !lastResortModelLimitSoft(ctx, acc, g.model, false):
		return "model_rate_limited", shareSoftRecovery(acc, g.model, now, resiliencePenaltyOf, shareModelLimitEnd(ctx, acc, g.model, now))
	case !s.isAccountSchedulableForQuota(acc):
		return "quota_exceeded", time.Time{}
	case !s.isAccountSchedulableForWindowCost(ctx, acc, sticky):
		return "window_cost", time.Time{}
	case !s.isAccountSchedulableForRPM(ctx, acc, sticky):
		return "rpm_limit", time.Time{}
	}
	return "", time.Time{}
}

// resiliencePenaltyFunc rates an account's restrictions as gateway
// resilience does (resiliencePenaltyOf, or the OpenAI variant).
type resiliencePenaltyFunc func(*Account, string, time.Time) (resilience.PenaltyTier, time.Time, string)

// shareLegacySoftSpillMax is the soft-spill bound of 0.3.0 (its default
// policy value), kept while gateway resilience is off.
const shareLegacySoftSpillMax = 300 * time.Second

// shareSoftRecovery is when a soft-penalised account is expected back, or
// zero when its state is hard or the recovery is unknown. Share scheduling
// keeps a session's binding through a short outage only on the strength of
// this time, so it agrees with gateway resilience on soft and hard states
// (design §4): a temporary state whose reason is empty or not a soft prefix
// is hard, and so is anything that lasts beyond the hard rate-limit
// threshold.
//
// stateEnd is when the state that rejected the account ends, as the
// adapter read it. With the resilience master switch off the 0.3.0 rule
// applies: any state that ends within shareLegacySoftSpillMax.
func shareSoftRecovery(acc *Account, model string, now time.Time, penalty resiliencePenaltyFunc, stateEnd time.Time) time.Time {
	if !resilience.Current().FailureOn() {
		if stateEnd.IsZero() || stateEnd.Sub(now) > shareLegacySoftSpillMax {
			return time.Time{}
		}
		return stateEnd
	}
	if penalty == nil {
		penalty = resiliencePenaltyOf
	}
	tier, recoverAt, _ := penalty(acc, model, now)
	if tier != resilience.TierSoft || recoverAt.IsZero() || recoverAt.Sub(now) > resilience.HardRateLimitThreshold {
		return time.Time{}
	}
	return recoverAt
}

// shareModelLimitEnd is when the account's rate limit for the model ends;
// zero when it is not limited.
func shareModelLimitEnd(ctx context.Context, acc *Account, model string, now time.Time) time.Time {
	if remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, model); remaining > 0 {
		return now.Add(remaining)
	}
	return time.Time{}
}

// shareAccountStateReason maps Account.IsSchedulable onto reason codes and
// the expected recovery time of temporary states.
func shareAccountStateReason(a *Account, now time.Time) (gatewayCandidateReason, time.Time) {
	switch {
	case a.Status == StatusDisabled:
		return "disabled", time.Time{}
	case !a.IsActive():
		return "inactive", time.Time{}
	case !a.Schedulable:
		return "not_schedulable", time.Time{}
	case a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt):
		return "expired", time.Time{}
	case a.OverloadUntil != nil && now.Before(*a.OverloadUntil):
		return "overloaded", *a.OverloadUntil
	case a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt):
		return "rate_limited", *a.RateLimitResetAt
	case a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil):
		if resilience.Current().FailureOn() && resilience.IsBalancePause(a.TempUnschedulableReason) {
			// Out of balance at the relay is a hard state: the session moves
			// at once instead of waiting out the pause. With resilience off
			// it is an ordinary pause, as in 0.3.0.
			return "balance_exhausted", time.Time{}
		}
		return "temp_unschedulable", *a.TempUnschedulableUntil
	case a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded():
		return "quota_exceeded", time.Time{}
	}
	return "not_schedulable", time.Time{}
}
