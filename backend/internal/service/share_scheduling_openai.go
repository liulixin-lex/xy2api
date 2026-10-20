package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const openAIAccountScheduleLayerShare = "share"

// openAIShareSelection is an OpenAI-compatible request to place.
type openAIShareSelection struct {
	groupID          *int64
	previousResponse string
	sessionHash      string
	model            string
	excluded         map[int64]struct{}
	transport        OpenAIUpstreamTransport
	capability       OpenAIEndpointCapability
	platform         string
	guardianParentID int64
	// preserveBinding leaves the session binding to the guardian parent.
	preserveBinding bool
}

// withOpenAIShareTrace starts the selection trace of text requests; the
// served account is recorded when the selection returns.
func (s *OpenAIGatewayService) withOpenAIShareTrace(ctx context.Context, groupID *int64, sessionHash string, excludedIDs map[int64]struct{}, imageCapability OpenAIImagesCapability) context.Context {
	if s.shareScheduling == nil || imageCapability != "" {
		return ctx
	}
	ctx, _ = s.shareScheduling.withSelectionTrace(ctx, derefGroupID(groupID), sessionHash, len(excludedIDs) > 0)
	return ctx
}

// shareSchedulingHook runs inside the original OpenAI selection once the
// quota, privacy and profit contexts and the guardian parent are resolved.
// Compact and image requests keep the original scheduler, whose tiered
// capability retries have no share equivalent; sessions owned by quality
// routing stay with it.
func (s *OpenAIGatewayService) shareSchedulingHook(ctx context.Context, in openAIShareSelection, requireCompact bool, imageCapability OpenAIImagesCapability) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	svc := s.shareScheduling
	if svc == nil || requireCompact || imageCapability != "" {
		return nil, OpenAIAccountScheduleDecision{}, false, nil
	}
	gp := svc.groupPolicy(derefGroupID(in.groupID))
	if gp == nil || s.qualityOwnsSticky(ctx, in.groupID, in.sessionHash) {
		return nil, OpenAIAccountScheduleDecision{}, false, nil
	}
	selection, decision, err := s.selectAccountWithShareScheduling(ctx, gp, in)
	return selection, decision, true, err
}

// selectAccountWithShareScheduling places one OpenAI-compatible request of
// a share group. Response continuations and the guardian parent express
// ownership of upstream state and are honoured before any placement.
func (s *OpenAIGatewayService) selectAccountWithShareScheduling(ctx context.Context, gp *shareGroupPolicy, in openAIShareSelection) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	start := time.Now()
	svc := s.shareScheduling
	decision := OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerShare}
	selection, layer, err := s.selectOpenAIShareOwner(ctx, in)
	if err != nil {
		return nil, decision, err
	}
	if selection != nil {
		decision.Layer = layer
		decision.StickyPreviousHit = layer == openAIAccountScheduleLayerPreviousResponse
		decision.StickySessionHit = layer == openAIAccountScheduleLayerGuardianParent
		decision.SelectedAccountID, decision.SelectedAccountType = selection.Account.ID, selection.Account.Type
		decision.LatencyMs = time.Since(start).Milliseconds()
		if trace := shareSelectionTraceFrom(ctx); trace != nil {
			trace.reason = sharesched.ReasonOwnerContinuation
		}
		svc.recordDecision(shareDecisionRecord{gp: gp, source: sharesched.KeyContinuation},
			sharesched.Decision{Reason: sharesched.ReasonOwnerContinuation, Target: selection.Account.ID, Ideal: selection.Account.ID},
			&shareOutcome{served: selection.Account.ID, reason: sharesched.ReasonOwnerContinuation, attempts: 1})
		return selection, decision, nil
	}
	if s.checkChannelPricingRestriction(ctx, in.groupID, in.model) {
		return nil, decision, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, in.model)
	}

	sessionHash := in.sessionHash
	if in.preserveBinding {
		sessionHash = ""
	}
	key, _ := shareAffinityKey(&gp.policy, sessionHash)
	cacheKey := ""
	if key != "" {
		cacheKey = s.openAISessionCacheKey(sessionHash)
	}
	binding, legacy := s.readOpenAIShareBinding(ctx, gp, cacheKey, sessionHash)
	cands, accounts, stats, err := s.shareOpenAICandidates(ctx, in, binding.AccountID)
	if err != nil {
		return nil, decision, err
	}
	if len(accounts) == 0 {
		return nil, decision, noAvailableOpenAISelectionError(in.model, false, stats.summary(""))
	}
	req, source := svc.newRequest(ctx, gp, in.platform, in.model, sessionHash, binding)
	rec := shareDecisionRecord{gp: gp, source: source, idle: req.Binding.Idle, cache: req.Cache}
	exec := &shareExecutor{ctx: ctx, gp: gp, cache: s.cache, concurrency: s.concurrencyService, cfg: s.schedulingConfig(),
		key: cacheKey, accounts: accounts, createOnRefresh: legacy, loadGate: true, loadOrder: shareLoadOrder(cands),
		limit: func(acc *Account) int { return s.codexEffectiveConcurrency(ctx, acc.ID, acc.Concurrency) },
		verify: func(acc *Account) (*Account, sharesched.Reason) {
			return s.verifyOpenAIShareAccount(ctx, in, acc)
		},
		admit: func(*Account) bool { return true },
		newResult: func(acc *Account, acquired bool, release func(), wait *AccountWaitPlan) (*AccountSelectionResult, error) {
			return attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: acc, Acquired: acquired, ReleaseFunc: release, WaitPlan: wait}), nil
		}}
	out, d := exec.run(&rec, req, cands, nil)
	for _, c := range cands {
		if c.Eligible && !c.Excluded {
			decision.CandidateCount++
		}
	}
	decision.LatencyMs = time.Since(start).Milliseconds()
	if out == nil {
		svc.recordDecision(rec, d, &shareOutcome{reason: d.Reason, attempts: exec.attempts, skipped: exec.skipped})
		for _, reason := range exec.skipped {
			stats.exclude(string(reason))
		}
		return nil, decision, noAvailableOpenAISelectionError(in.model, false, stats.summary("share_plan_exhausted"))
	}
	svc.recordDecision(rec, d, out)
	if out.result != nil && out.result.Account != nil {
		decision.SelectedAccountID, decision.SelectedAccountType = out.result.Account.ID, out.result.Account.Type
		decision.StickySessionHit = d.Bound > 0 && out.served == d.Bound
	}
	return out.result, decision, out.err
}

// shareOpenAILastResort is last-resort selection in a share group: the
// share plan over the group with soft penalties relaxed, served with the
// adaptive Codex limit and the binding intent of normal placement.
func (s *OpenAIGatewayService) shareOpenAILastResort(ctx context.Context, gp *shareGroupPolicy, req OpenAIAccountScheduleRequest, avoided map[int64]struct{}) *AccountSelectionResult {
	accounts, err := s.resilience.groupAccountsForLastResort(ctx, s.accountRepo, *req.GroupID)
	if err != nil {
		slog.Warn("resilience.last_resort_list_failed", "group_id", *req.GroupID, "error", err)
		return nil
	}
	svc := s.shareScheduling
	platform := NormalizeOpenAICompatiblePlatform(req.Platform)
	sessionHash := req.SessionHash
	if preserveOpenAIGuardianParentBinding(ctx, sessionHash) {
		sessionHash = ""
	}
	key, _ := shareAffinityKey(&gp.policy, sessionHash)
	cacheKey := ""
	if key != "" {
		cacheKey = s.openAISessionCacheKey(sessionHash)
	}
	binding, legacy := s.readOpenAIShareBinding(ctx, gp, cacheKey, sessionHash)
	gates := s.newOpenAIShareGates(withResilienceLastResortGate(ctx), openAIShareSelection{groupID: req.GroupID,
		model: req.RequestedModel, transport: req.RequiredTransport, capability: req.RequiredCapability, platform: platform})
	now := time.Now()
	round := svc.newShareLastResortRound(accounts, req.RequestedModel, req.ExcludedIDs, avoided, binding.AccountID, now,
		func(acc *Account) bool { return acc.Platform == platform && acc.IsOpenAICompatible() },
		s.resilienceOpenAIPenaltyOf,
		func(acc *Account, _ bool) sharesched.Reason {
			reason, _ := gates.candidateReason(acc, now)
			return reason
		})
	shareReq, _ := svc.newRequest(ctx, gp, platform, req.RequestedModel, sessionHash, binding)
	exec := &shareExecutor{ctx: ctx, gp: gp, cache: s.cache, concurrency: s.concurrencyService, cfg: s.schedulingConfig(),
		key: cacheKey, accounts: round.accounts,
		createOnRefresh: legacy, reason: sharesched.ReasonLastResortSoft,
		limit: func(acc *Account) int { return s.codexEffectiveConcurrency(ctx, acc.ID, acc.Concurrency) },
		verify: func(acc *Account) (*Account, sharesched.Reason) {
			// The plan used a 2 s group list (review R09).
			if fresh, ok := s.resilience.verifyLastResortAccount(ctx, s.accountRepo, *req.GroupID, acc, req.RequestedModel, s.resilienceOpenAIPenaltyOf); ok {
				return fresh, ""
			}
			return nil, sharesched.ReasonIneligible
		},
		admit: func(*Account) bool { return true },
		newResult: func(acc *Account, acquired bool, release func(), wait *AccountWaitPlan) (*AccountSelectionResult, error) {
			return attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: acc, Acquired: acquired, ReleaseFunc: release, WaitPlan: wait}), nil
		}}
	out := exec.serve(round.plan(shareReq))
	if out == nil || out.err != nil || out.result == nil {
		return nil
	}
	s.noteOpenAILastResort(ctx, req.GroupID, out.result.Account, round.recoverAt[out.served])
	return out.result
}

// selectOpenAIShareOwner serves a response continuation on the account that
// owns the previous response, or a guardian request on its parent's account.
func (s *OpenAIGatewayService) selectOpenAIShareOwner(ctx context.Context, in openAIShareSelection) (*AccountSelectionResult, string, error) {
	selection, hit, err := s.selectLegacyAccountByPreviousResponse(ctx, in.groupID, in.previousResponse, in.sessionHash, in.model,
		in.excluded, in.transport, in.capability, "", false, in.platform)
	if err != nil {
		return nil, "", err
	}
	if hit {
		return selection, openAIAccountScheduleLayerPreviousResponse, nil
	}
	if in.guardianParentID <= 0 {
		return nil, "", nil
	}
	if s.checkChannelPricingRestriction(ctx, in.groupID, in.model) {
		return nil, "", fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, in.model)
	}
	scheduler := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	selection, _, err = scheduler.selectBySessionHash(ctx, OpenAIAccountScheduleRequest{
		GroupID:               in.groupID,
		Platform:              in.platform,
		SessionHash:           in.sessionHash,
		StickyAccountID:       in.guardianParentID,
		PreserveStickyBinding: true,
		RequestedModel:        in.model,
		RequiredTransport:     in.transport,
		RequiredCapability:    in.capability,
		ExcludedIDs:           in.excluded,
		RequirePrivacySet:     s.openAIGroupRequiresPrivacySet(ctx, in.groupID),
	})
	if err != nil || selection == nil || selection.Account == nil {
		return nil, "", err
	}
	return selection, openAIAccountScheduleLayerGuardianParent, nil
}

// readOpenAIShareBinding reads the session binding, falling back to the
// legacy hash key while dual hashing is in effect. A legacy hit reports
// legacy so the plan creates the primary binding.
func (s *OpenAIGatewayService) readOpenAIShareBinding(ctx context.Context, gp *shareGroupPolicy, cacheKey, sessionHash string) (sharesched.Binding, bool) {
	binding := s.shareScheduling.readBinding(ctx, s.cache, gp, cacheKey)
	if binding.AccountID > 0 || cacheKey == "" || s.cache == nil || !s.openAISessionHashReadOldFallbackEnabled() {
		return binding, false
	}
	legacyKey := s.openAILegacySessionCacheKey(ctx, sessionHash)
	if legacyKey == "" {
		return binding, false
	}
	accountID, err := s.cache.GetSessionAccountID(ctx, gp.groupID, legacyKey)
	if err != nil || accountID <= 0 {
		return binding, false
	}
	return sharesched.Binding{AccountID: accountID}, true
}

// shareOpenAICandidates lists the group's accounts and runs the original
// OpenAI gates on each, keeping the failing gate as the reason.
func (s *OpenAIGatewayService) shareOpenAICandidates(ctx context.Context, in openAIShareSelection, boundID int64) ([]sharesched.Candidate, map[int64]*Account, openAISelectionFilterStats, error) {
	listed, err := s.listSchedulableAccounts(ctx, in.groupID, in.platform)
	if err != nil {
		return nil, nil, openAISelectionFilterStats{}, err
	}
	stats := openAISelectionFilterStats{pool: len(listed)}
	gates := s.newOpenAIShareGates(ctx, in)
	now := time.Now()
	accounts := make(map[int64]*Account, len(listed))
	cands := make([]sharesched.Candidate, 0, len(listed)+1)
	for i := range listed {
		acc := &listed[i]
		accounts[acc.ID] = acc
		c := sharesched.Candidate{AccountID: acc.ID, Platform: acc.Platform, Class: shareAccountClass(acc),
			Priority: acc.Priority, Weight: shareWeight(acc)}
		if _, excluded := in.excluded[acc.ID]; excluded {
			c.Excluded, c.Eligible = true, true
			if resilience.StateFrom(ctx).Avoided(acc.ID) {
				c.Reason = sharesched.ReasonResilFailureStreak
			}
			stats.exclude("excluded")
			cands = append(cands, c)
			continue
		}
		c.Reason, c.RecoverAt = gates.candidateReason(acc, now)
		c.Eligible = c.Reason == ""
		if !c.Eligible {
			stats.exclude(string(c.Reason))
		}
		c.StableSince = s.shareScheduling.observeEligibility(acc.ID, c.Eligible, c.Reason, now)
		cands = append(cands, c)
	}
	if boundID > 0 && accounts[boundID] == nil {
		if _, excluded := in.excluded[boundID]; !excluded {
			if acc := gates.lookup(boundID); acc != nil && acc.Platform == in.platform && s.openAIAccountMatchesSchedulingGroup(acc, in.groupID) {
				if c, ok := s.shareScheduling.shareMissingBoundCandidate(acc, in.model, now, s.resilienceOpenAIPenaltyOf); ok {
					cands = append(cands, c)
				}
			}
		}
	}
	return cands, accounts, stats, nil
}

// verifyOpenAIShareAccount re-reads a planned account from the snapshot and
// the database, exactly as the original scheduler does before serving.
func (s *OpenAIGatewayService) verifyOpenAIShareAccount(ctx context.Context, in openAIShareSelection, acc *Account) (*Account, sharesched.Reason) {
	fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, acc, in.platform, in.model, false, in.capability)
	if fresh != nil {
		fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, in.groupID, in.platform, in.model, false, in.capability)
	}
	if fresh == nil {
		return nil, sharesched.ReasonIneligible
	}
	if reason := s.newOpenAIShareGates(ctx, in).requestReason(fresh, time.Now()); reason != "" {
		return nil, reason
	}
	return fresh, ""
}

// openAIShareGates evaluates the original OpenAI scheduling gates for one
// selection.
type openAIShareGates struct {
	s            *OpenAIGatewayService
	ctx          context.Context
	in           openAIShareSelection
	privacy      bool
	channelCheck bool
	parents      map[int64]*Account
}

func (s *OpenAIGatewayService) newOpenAIShareGates(ctx context.Context, in openAIShareSelection) *openAIShareGates {
	return &openAIShareGates{s: s, ctx: ctx, in: in, privacy: s.openAIGroupRequiresPrivacySet(ctx, in.groupID),
		channelCheck: s.needsUpstreamChannelRestrictionCheck(ctx, in.groupID)}
}

// lookup reads an account from the snapshot, falling back to the database.
func (g *openAIShareGates) lookup(id int64) *Account {
	if acc, ok := g.parents[id]; ok {
		return acc
	}
	var acc *Account
	if g.s.schedulerSnapshot != nil {
		acc, _ = g.s.schedulerSnapshot.GetAccount(g.ctx, id)
	}
	if acc == nil && g.s.accountRepo != nil {
		acc, _ = g.s.accountRepo.GetByID(g.ctx, id)
	}
	if g.parents == nil {
		g.parents = map[int64]*Account{}
	}
	g.parents[id] = acc
	return acc
}

// candidateReason mirrors the advanced scheduler's candidate filter, with the
// expected recovery of a soft penalty (shareSoftRecovery). In a last-resort
// context the caller has cleared the soft account states, and the
// scheduler's soft restrictions pass as in the original last resort: short
// per-model rate limits and in-memory runtime blocks.
func (g *openAIShareGates) candidateReason(acc *Account, now time.Time) (sharesched.Reason, time.Time) {
	if !acc.IsSchedulable() {
		reason, end := shareAccountStateReason(acc, now)
		return reason, shareSoftRecovery(acc, g.in.model, now, g.s.resilienceOpenAIPenaltyOf, end)
	}
	lastResort := resilienceLastResortGate(g.ctx)
	switch reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(g.ctx, acc, g.in.platform, g.in.model, false, g.in.capability); {
	case reason == "":
	case reason == "model_rate_limited" && lastResortModelLimitSoft(g.ctx, acc, g.in.model):
	case reason == "model_rate_limited":
		return sharesched.ReasonModelRateLimited, shareSoftRecovery(acc, g.in.model, now, g.s.resilienceOpenAIPenaltyOf, shareModelLimitEnd(g.ctx, acc, g.in.model, now))
	case reason == "not_schedulable":
		reason, end := shareAccountStateReason(acc, now)
		return reason, shareSoftRecovery(acc, g.in.model, now, g.s.resilienceOpenAIPenaltyOf, end)
	case strings.HasPrefix(reason, "quota_auto_pause"):
		return sharesched.ReasonQuotaAutoPause, time.Time{}
	case reason == "platform_mismatch":
		return sharesched.ReasonPlatformMismatch, time.Time{}
	case reason == "capability_mismatch":
		return sharesched.ReasonCapabilityMismatch, time.Time{}
	default:
		return sharesched.ReasonModelUnsupported, time.Time{}
	}
	switch {
	case g.privacy && !acc.IsPrivacySet():
		return sharesched.ReasonPrivacyRequired, time.Time{}
	case !lastResort && g.s.isOpenAIAccountRequestRuntimeBlocked(acc, g.in.model, false):
		return sharesched.ReasonRuntimeBlocked, time.Time{}
	case !lastResort && g.s.isOpenAIProxyStreamQuarantined(g.ctx, acc):
		return sharesched.ReasonProxyQuarantined, time.Time{}
	case !parentHealthyForShadow(acc, g.lookup):
		return sharesched.ReasonShadowParent, time.Time{}
	}
	if reason := g.requestReason(acc, now); reason != "" {
		return reason, time.Time{}
	}
	if vetoed, _ := openAIProfitControlVetoReason(g.ctx, acc); vetoed {
		return sharesched.ReasonProfitGate, time.Time{}
	}
	return "", time.Time{}
}

// requestReason covers the request-specific gates that the snapshot and
// database rechecks do not repeat. Last resort treats the in-memory Grok
// blocks like the other runtime blocks.
func (g *openAIShareGates) requestReason(acc *Account, now time.Time) sharesched.Reason {
	if g.channelCheck && g.in.groupID != nil && g.s.isUpstreamModelRestrictedByChannel(g.ctx, *g.in.groupID, acc, g.in.model, false) {
		return sharesched.ReasonChannelRestricted
	}
	if !g.s.isOpenAIAccountTransportCompatible(acc, g.in.transport) {
		return sharesched.ReasonTransportMismatch
	}
	if acc.IsGrok() && strings.TrimSpace(g.in.model) != "" && !resilienceLastResortGate(g.ctx) {
		model := canonicalOpenAIAccountSchedulingModel(acc, g.in.model)
		if isGrokTeamModelRateLimited(acc, model, now) {
			return sharesched.ReasonModelRateLimited
		}
		if isGrokModelQuotaBlocked(acc.ID, model, now) {
			return sharesched.ReasonGrokQuota
		}
	}
	return ""
}
