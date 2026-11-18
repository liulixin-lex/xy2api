package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type gatewayCandidateReason string

// gatewayCandidateGates are the original Sub2API per-account gates of one
// gateway selection. Share candidates and last-resort selection both run
// accounts through them, so neither can miss a gate the other applies.
type gatewayCandidateGates struct {
	s                *GatewayService
	groupID          *int64
	platform         string
	useMixed         bool
	model            string
	channelCheck     bool
	profitAtDispatch bool
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
	case !g.profitAtDispatch && !s.isGatewayAccountProfitEligible(ctx, acc):
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

// gatewayWaitEligibility freezes the policy of the branch that selected this
// account. Waiting plans mask any cost/RPM prefetch inherited from selection.
type gatewayWaitEligibility struct {
	ctx                                   context.Context
	groupID                               *int64
	platform, model                       string
	useMixed, sticky, lastResort          bool
	checkGroup, skipChannel, checkPrivacy bool
}

type legacyWaitEligibilityKey struct{}

func (s *GatewayService) gatewayWaitCriteria(ctx context.Context, groupID *int64, platform, model string, mixed bool) gatewayWaitEligibility {
	// In simple mode the snapshot lists every account of the platform. The
	// repository-only mixed list still scopes a non-nil group.
	checkGroup := s.cfg == nil || s.cfg.RunMode != config.RunModeSimple || (mixed && groupID != nil)
	if s.schedulerSnapshot != nil {
		checkGroup = !s.schedulerSnapshot.isRunModeSimple()
	}
	ctx = context.WithValue(ctx, windowCostPrefetchContextKey, nil)
	ctx = context.WithValue(ctx, rpmPrefetchContextKey, nil)
	return gatewayWaitEligibility{ctx: ctx, groupID: groupID, platform: platform, model: model, useMixed: mixed, checkGroup: checkGroup}
}

func (w gatewayWaitEligibility) forSticky() gatewayWaitEligibility {
	w.sticky = true
	return w
}

func noteLegacyWaitChoice(ctx context.Context, sticky, skipChannel bool) {
	if w, ok := ctx.Value(legacyWaitEligibilityKey{}).(*gatewayWaitEligibility); ok {
		w.sticky = sticky
		w.skipChannel = skipChannel
		w.checkPrivacy = !sticky && w.groupID != nil
		w.checkGroup = w.checkGroup || sticky
	}
}

// GatewayAccountAfterSlot returns nil for a pre-dispatch eligibility loss.
// A profit veto retains the account and its distinct veto reason.
func (s *GatewayService) GatewayAccountAfterSlot(ctx context.Context, sel *AccountSelectionResult) (*Account, bool, string) {
	if sel.Acquired {
		return s.GatewayProfitControlVetoLatest(ctx, sel.Account)
	}
	w := sel.WaitPlan.gatewayEligibility
	fresh, err := s.accountRepo.GetByID(ctx, sel.Account.ID)
	if errors.Is(err, ErrAccountNotFound) {
		return nil, false, "account_deleted"
	}
	if err != nil {
		slog.Warn("gateway.wait_account_refresh_failed", "group_id", derefGroupID(w.groupID), "account_id", sel.Account.ID,
			"platform", w.platform, "error_class", "storage_read")
		return s.GatewayProfitControlVetoLatest(ctx, sel.Account)
	}
	if fresh == nil || (w.checkGroup && !s.isAccountInGroup(fresh, w.groupID)) {
		return nil, false, "account_missing_or_moved"
	}
	gateCtx := w.ctx
	now := time.Now()
	if w.lastResort {
		tier, _, _ := resiliencePenaltyOf(fresh, w.model, now)
		if tier == resilience.TierHard || iqGateBlocksRequest(fresh, w.model, false) {
			return nil, false, "hard_penalty"
		}
		fresh = withoutSoftPenalties(*fresh, now)
		gateCtx = withResilienceLastResortGate(gateCtx)
	} else if s.isAccountBlockedBySchedulingThreshold(gateCtx, fresh) || (fresh.IsGrok() && len(s.filterGrokFreeQuotaAccountsForGateway(gateCtx, []Account{*fresh})) == 0) {
		return nil, false, "scheduling_threshold"
	}
	if s.checkChannelPricingRestriction(gateCtx, w.groupID, w.model) {
		return nil, false, "channel_restricted"
	}
	gates := s.newGatewayCandidateGates(gateCtx, w.groupID, w.platform, w.useMixed, w.model)
	gates.channelCheck = gates.channelCheck && !w.skipChannel
	gates.profitAtDispatch = true
	if reason, _ := gates.candidateReason(gateCtx, fresh, w.sticky, now); reason != "" {
		return nil, false, string(reason)
	}
	if w.checkPrivacy && !fresh.IsPrivacySet() && s.groupRepo != nil {
		group, err := s.groupRepo.GetByIDLite(ctx, *w.groupID)
		if errors.Is(err, ErrGroupNotFound) || (err == nil && group == nil) {
			return nil, false, "group_deleted"
		}
		if err != nil {
			slog.Warn("gateway.wait_group_refresh_failed", "group_id", *w.groupID, "account_id", fresh.ID, "platform", w.platform, "error_class", "storage_read")
		} else if group.RequirePrivacySet {
			return nil, false, "privacy_required"
		}
	}
	// The authoritative row already includes current prices and membership.
	return profitControlVetoLatest(ctx, fresh, nil)
}
