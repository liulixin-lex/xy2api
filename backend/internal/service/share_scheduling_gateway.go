package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// shareSchedulingHook runs at the top of the original gateway selection,
// after the group, profit gate and channel restriction are resolved. For
// share groups it places the request and returns handled; other groups only
// get a trace.
func (s *GatewayService) shareSchedulingHook(ctx context.Context, group *Group, groupID *int64, sessionHash, requestedModel string, excludedIDs map[int64]struct{}) (context.Context, *AccountSelectionResult, bool, error) {
	svc := s.shareScheduling
	if svc == nil {
		return ctx, nil, false, nil
	}
	ctx, trace := svc.withSelectionTrace(ctx, derefGroupID(groupID), sessionHash, len(excludedIDs) > 0)
	gp := svc.groupPolicy(trace.groupID)
	if gp == nil {
		return ctx, nil, false, nil
	}
	result, err := s.selectAccountWithShareScheduling(ctx, gp, group, groupID, sessionHash, requestedModel, excludedIDs)
	return ctx, result, true, err
}

// shareRecordSelection runs for every gateway selection result.
func (s *GatewayService) shareRecordSelection(ctx context.Context, account *Account) {
	s.shareScheduling.recordSelection(ctx, account)
}

// shareGatewayInputs are the resolved candidates of one gateway selection.
type shareGatewayInputs struct {
	platform string
	accounts map[int64]*Account
	cands    []sharesched.Candidate
	routing  []int64
	restrict int
	listed   int
}

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
func (g gatewayCandidateGates) candidateReason(ctx context.Context, acc *Account, sticky bool, now time.Time) (sharesched.Reason, time.Time) {
	s := g.s
	switch {
	case !s.isAccountSchedulableForSelection(acc):
		reason, end := shareAccountStateReason(acc, now)
		return reason, shareSoftRecovery(acc, g.model, now, resiliencePenaltyOf, end)
	case !s.isGatewayAccountProfitEligible(ctx, acc):
		return sharesched.ReasonProfitGate, time.Time{}
	case !s.isAccountAllowedForPlatform(acc, g.platform, g.useMixed):
		return sharesched.ReasonPlatformMismatch, time.Time{}
	case g.model != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, g.model):
		return sharesched.ReasonModelUnsupported, time.Time{}
	case g.channelCheck && s.isUpstreamModelRestrictedByChannel(ctx, *g.groupID, acc, g.model):
		return sharesched.ReasonChannelRestricted, time.Time{}
	case !s.isAccountSchedulableForModelSelection(ctx, acc, g.model) && !lastResortModelLimitSoft(ctx, acc, g.model):
		return sharesched.ReasonModelRateLimited, shareSoftRecovery(acc, g.model, now, resiliencePenaltyOf, shareModelLimitEnd(ctx, acc, g.model, now))
	case !s.isAccountSchedulableForQuota(acc):
		return sharesched.ReasonQuotaExceeded, time.Time{}
	case !s.isAccountSchedulableForWindowCost(ctx, acc, sticky):
		return sharesched.ReasonWindowCost, time.Time{}
	case !s.isAccountSchedulableForRPM(ctx, acc, sticky):
		return sharesched.ReasonRPMLimit, time.Time{}
	}
	return "", time.Time{}
}

// shareGatewayCandidates lists the group's accounts and runs the original
// Sub2API gates on each, keeping the failing gate as the reason.
func (s *GatewayService) shareGatewayCandidates(ctx context.Context, group *Group, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, boundID int64) (context.Context, *shareGatewayInputs, error) {
	platform, hasForcePlatform, err := s.resolvePlatform(ctx, groupID, group, requestedModel)
	if err != nil {
		return ctx, nil, err
	}
	accounts, useMixed, err := s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
	if err != nil {
		return ctx, nil, err
	}
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)
	gates := s.newGatewayCandidateGates(ctx, groupID, platform, useMixed, requestedModel)
	now := time.Now()
	in := &shareGatewayInputs{platform: platform, accounts: make(map[int64]*Account, len(accounts)),
		cands: make([]sharesched.Candidate, 0, len(accounts)), listed: len(accounts)}
	for i := range accounts {
		acc := &accounts[i]
		in.accounts[acc.ID] = acc
		c := s.shareScheduling.shareCandidate(derefGroupID(groupID), acc, now)
		if _, excluded := excludedIDs[acc.ID]; excluded {
			c.Excluded, c.Eligible = true, true
			if resilience.StateFrom(ctx).Avoided(acc.ID) {
				c.Reason = sharesched.ReasonResilFailureStreak
			}
			in.cands = append(in.cands, c)
			continue
		}
		c.Reason, c.RecoverAt = gates.candidateReason(ctx, acc, acc.ID == boundID, now)
		c.Eligible = c.Reason == ""
		if c.Reason == sharesched.ReasonChannelRestricted {
			in.restrict++
		}
		c.StableSince = s.shareScheduling.observeEligibility(acc.ID, c.Eligible, c.Reason, now)
		in.cands = append(in.cands, c)
	}
	if boundID > 0 && in.accounts[boundID] == nil {
		if _, excluded := excludedIDs[boundID]; !excluded {
			if c, ok := s.shareScheduling.shareMissingBoundCandidate(derefGroupID(groupID), s.lookupShareBoundAccount(ctx, boundID), requestedModel, now, resiliencePenaltyOf); ok {
				in.cands = append(in.cands, c)
			}
		}
	}
	if group != nil && requestedModel != "" && modelRoutingAppliesToPlatform(platform, group.Platform) {
		in.routing = group.GetRoutingAccountIDs(requestedModel)
	}
	return ctx, in, nil
}

func (s *GatewayService) lookupShareBoundAccount(ctx context.Context, accountID int64) *Account {
	var acc *Account
	var err error
	switch {
	case s.schedulerSnapshot != nil:
		acc, err = s.schedulerSnapshot.GetAccount(ctx, accountID)
	case s.accountRepo != nil:
		acc, err = s.accountRepo.GetByID(ctx, accountID)
	}
	if err != nil {
		return nil
	}
	return acc
}

// selectAccountWithShareScheduling places one request of a share group.
func (s *GatewayService) selectAccountWithShareScheduling(ctx context.Context, gp *shareGroupPolicy, group *Group, groupID *int64, sessionHash, requestedModel string, excludedIDs map[int64]struct{}) (*AccountSelectionResult, error) {
	svc := s.shareScheduling
	key, _ := shareAffinityKey(&gp.policy, sessionHash)
	binding := svc.readBinding(ctx, s.cache, gp, key)
	ctx, in, err := s.shareGatewayCandidates(ctx, group, groupID, requestedModel, excludedIDs, binding.AccountID)
	if err != nil {
		return nil, err
	}
	if in.listed == 0 {
		return nil, ErrNoAvailableAccounts
	}
	req, _ := svc.newRequest(ctx, gp, in.platform, requestedModel, sessionHash, binding)
	rec := shareDecisionRecord{gp: gp, idle: req.Binding.Idle, cache: req.Cache}
	exec := &shareExecutor{ctx: ctx, gp: gp, cache: s.cache, concurrency: s.concurrencyService, cfg: s.schedulingConfig(),
		key: key, accounts: in.accounts, loadGate: true, loadOrder: shareLoadOrder(in.cands),
		limit: func(acc *Account) int { return acc.Concurrency },
		admit: func(acc *Account) bool { return s.checkAndRegisterSession(ctx, acc, sessionHash) },
		newResult: func(acc *Account, acquired bool, release func(), wait *AccountWaitPlan) (*AccountSelectionResult, error) {
			return s.newSelectionResult(ctx, acc, acquired, release, wait)
		}}
	out, decision := exec.run(&rec, req, in.cands, in.routing)
	if out == nil {
		svc.recordDecision(rec, decision, &shareOutcome{reason: decision.Reason, attempts: exec.attempts, skipped: exec.skipped})
		if len(decision.Steps) == 0 && in.restrict > 0 {
			return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
		}
		return nil, ErrNoAvailableAccounts
	}
	svc.recordDecision(rec, decision, out)
	return out.result, out.err
}

// shareGatewayLastResort is last-resort selection in a share group: the
// share plan over the group with soft penalties relaxed, served by the same
// executor as normal placement (slots, session limits, binding intent).
func (s *GatewayService) shareGatewayLastResort(ctx context.Context, gp *shareGroupPolicy, group *Group, groupID *int64, sessionHash, requestedModel string, excludedIDs, avoided map[int64]struct{}) *AccountSelectionResult {
	platform, hasForcePlatform, err := s.resolvePlatform(ctx, groupID, group, requestedModel)
	if err != nil {
		return nil
	}
	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	accounts, err := s.resilience.groupAccountsForLastResort(ctx, s.accountRepo, *groupID)
	if err != nil {
		slog.Warn("resilience.last_resort_list_failed", "group_id", *groupID, "error", err)
		return nil
	}
	svc := s.shareScheduling
	key, _ := shareAffinityKey(&gp.policy, sessionHash)
	binding := svc.readBinding(ctx, s.cache, gp, key)
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)
	gates := s.newGatewayCandidateGates(ctx, groupID, platform, useMixed, requestedModel)
	gateCtx := withResilienceLastResortGate(ctx)
	now := time.Now()
	round := svc.newShareLastResortRound(gp.groupID, accounts, requestedModel, excludedIDs, avoided, binding.AccountID, now, nil, nil,
		func(acc *Account, sticky bool) sharesched.Reason {
			reason, _ := gates.candidateReason(gateCtx, acc, sticky, now)
			return reason
		})
	req, _ := svc.newRequest(ctx, gp, platform, requestedModel, sessionHash, binding)
	exec := &shareExecutor{ctx: ctx, gp: gp, cache: s.cache, concurrency: s.concurrencyService, cfg: s.schedulingConfig(),
		key: key, accounts: round.accounts,
		reason: sharesched.ReasonLastResortSoft,
		limit:  func(acc *Account) int { return acc.Concurrency },
		verify: func(acc *Account) (*Account, sharesched.Reason) {
			// The plan used a 2 s group list (review R09).
			if fresh, ok := s.resilience.verifyLastResortAccount(ctx, s.accountRepo, *groupID, acc, requestedModel, resiliencePenaltyOf); ok {
				return fresh, ""
			}
			return nil, sharesched.ReasonIneligible
		},
		admit: func(acc *Account) bool {
			if s.checkAndRegisterSession(ctx, acc, sessionHash) {
				return true
			}
			resilience.StateFrom(ctx).MarkLastResortSessionLimited()
			return false
		},
		newResult: func(acc *Account, acquired bool, release func(), wait *AccountWaitPlan) (*AccountSelectionResult, error) {
			return s.newSelectionResult(ctx, acc, acquired, release, wait)
		}}
	out := exec.serve(round.plan(req))
	if out == nil || out.err != nil || out.result == nil {
		return nil
	}
	s.noteLastResort(ctx, groupID, out.result.Account, round.recoverAt[out.served])
	return out.result
}
