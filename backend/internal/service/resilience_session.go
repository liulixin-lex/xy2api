package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// lastResortCandidateTTL bounds how long the group account list used by
// last-resort selection is reused; it only runs when normal selection found
// no account, so a short cache keeps bursts off the database.
const lastResortCandidateTTL = 2 * time.Second

// resiliencePenaltyOf returns the penalty tier of an account for a model
// (design §3.1): hard states are never used; soft states only as a last
// resort. recoverAt is when the latest soft restriction ends. Timed hard
// states retain the later of their own end and the model breaker end.
func resiliencePenaltyOf(a *Account, model string, now time.Time) (resilience.PenaltyTier, time.Time, string) {
	if a == nil || !a.IsActive() || !a.Schedulable {
		return resilience.TierHard, time.Time{}, "unavailable"
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return resilience.TierHard, time.Time{}, "expired"
	}
	if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
		return resilience.TierHard, time.Time{}, "quota_exceeded"
	}
	tier := resilience.TierNone
	var recoverAt time.Time
	reason := ""
	soft := func(until time.Time, why string) {
		tier = resilience.TierSoft
		if until.After(recoverAt) {
			recoverAt = until
			reason = why
		}
	}
	modelBreakerEnd := Resilience().modelBreakerEnd(a, model, now)
	hard := func(until time.Time, why string) (resilience.PenaltyTier, time.Time, string) {
		if modelBreakerEnd.After(until) {
			until = modelBreakerEnd
		}
		return resilience.TierHard, until, why
	}
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) {
		if resilience.TempUnschedTier(a.TempUnschedulableReason) == resilience.TierHard {
			if resilience.IsBalancePause(a.TempUnschedulableReason) {
				return hard(*a.TempUnschedulableUntil, "balance_exhausted")
			}
			return hard(*a.TempUnschedulableUntil, "temp_unschedulable")
		}
		soft(*a.TempUnschedulableUntil, "temp_unschedulable")
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
		if a.RateLimitResetAt.Sub(now) > resilience.HardRateLimitThreshold {
			return hard(*a.RateLimitResetAt, "rate_limited")
		}
		soft(*a.RateLimitResetAt, "rate_limited")
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		soft(*a.OverloadUntil, "overloaded")
	}
	if model != "" {
		if !a.IsOpenAICompatible() {
			if !modelBreakerEnd.IsZero() {
				soft(modelBreakerEnd, "model_transient")
			}
		}
		if remaining := a.GetRateLimitRemainingTimeWithContext(context.Background(), model); remaining > 0 {
			if remaining > resilience.HardRateLimitThreshold {
				return hard(now.Add(remaining), "model_rate_limited")
			}
			soft(now.Add(remaining), "model_rate_limited")
		}
	}
	return tier, recoverAt, reason
}

// resilienceOpenAIPenaltyOf is resiliencePenaltyOf for the OpenAI scheduler.
// Its per-model breaker (upstream 5xx in a row block the model for 10 s,
// then 45 s) is a soft penalty (design §2) that the account row does not
// show: an account held back only by it is soft until the block ends, so
// last resort can use it. The in-process account block only holds while a
// persisted cooldown does, which resiliencePenaltyOf already reads; Codex
// ticket gates are no penalty and stay gates.
func (s *OpenAIGatewayService) resilienceOpenAIPenaltyOf(acc *Account, model string, now time.Time) (resilience.PenaltyTier, time.Time, string) {
	tier, recoverAt, why := resiliencePenaltyOf(acc, model, now)
	if tier == resilience.TierHard {
		return tier, recoverAt, why
	}
	if end := s.openAIModelBreakerEnd(acc, model, now); end.After(recoverAt) {
		return resilience.TierSoft, end, "model_transient"
	}
	return tier, recoverAt, why
}

// openAIModelBreakerEnd is when the per-model breaker on the account ends
// for the model; zero when it does not block.
func (s *OpenAIGatewayService) openAIModelBreakerEnd(acc *Account, model string, now time.Time) time.Time {
	state := s.getOpenAIAccountModelTransientState()
	if state == nil || acc == nil {
		return time.Time{}
	}
	key, ok := openAIAccountModelTransientKey(acc.ID, openAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(acc, model)))
	if !ok {
		return time.Time{}
	}
	state.mu.Lock()
	entry := state.entries[key]
	state.mu.Unlock()
	if now.Before(entry.blockUntil) {
		return entry.blockUntil
	}
	return time.Time{}
}

// resilienceKeepsSoftStickyBinding reports whether a bound account that
// cannot serve now keeps its session binding because its restriction is only
// soft (design §4.2). Hard states still clear the binding.
func resilienceKeepsSoftStickyBinding(account *Account, model string) bool {
	if account == nil || !resilience.Current().SessionOn() {
		return false
	}
	tier, _, _ := resiliencePenaltyOf(account, model, time.Now())
	return tier == resilience.TierSoft
}

// withoutSoftPenalties returns a copy of a soft-penalised account with its
// soft restrictions cleared, so the regular per-account gates can evaluate
// it for last-resort use. Hard fields stay untouched.
func withoutSoftPenalties(a Account, now time.Time) *Account {
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) &&
		resilience.TempUnschedTier(a.TempUnschedulableReason) == resilience.TierSoft {
		a.TempUnschedulableUntil = nil
		a.TempUnschedulableReason = ""
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) && a.RateLimitResetAt.Sub(now) <= resilience.HardRateLimitThreshold {
		a.RateLimitResetAt = nil
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		a.OverloadUntil = nil
	}
	return &a
}

// lastResortCandidate is one soft-penalised account eligible for last resort.
type lastResortCandidate struct {
	account   *Account
	recoverAt time.Time
	held      bool
}

// pickLastResort orders candidates by priority (lower first), then by the
// latency hold preference, then the earliest end of the penalty (design §4.3).
func pickLastResort(cands []lastResortCandidate) []lastResortCandidate {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].account.Priority != cands[j].account.Priority {
			return cands[i].account.Priority < cands[j].account.Priority
		}
		if cands[i].held != cands[j].held {
			return !cands[i].held
		}
		return cands[i].recoverAt.Before(cands[j].recoverAt)
	})
	return cands
}

// shareLastResortRound is the last-resort round of a share group (design
// §4.3): the share plan runs once more over the group with soft-penalised
// accounts, and accounts the session only avoids, counted as eligible, so
// tiers, drain, weights and cache domains apply exactly as in normal
// placement. Hard states and every other gate still exclude.
type shareLastResortRound struct {
	cands []sharesched.Candidate
	// accounts are the eligible accounts with their soft restrictions
	// cleared, as they serve.
	accounts  map[int64]*Account
	recoverAt map[int64]time.Time
	// keep is the bound account unless it is hard-penalised, avoided or
	// rejected by a gate: the binding stays with it whichever account
	// serves, as a soft-penalised owner keeps it in normal selection
	// (design §4.2).
	keep int64
	// rebind marks a binding to an account the session avoids: the
	// session is placed afresh and its binding moves to the serving account.
	rebind bool
}

// newShareLastResortRound evaluates the group's accounts for the round.
// include filters the accounts the scheduler considers at all; penalty is
// the scheduler's view of soft and hard states (resiliencePenaltyOf when
// nil); gate applies its original gates, in a last-resort context, to an
// account whose soft restrictions are cleared.
func (s *ShareSchedulingService) newShareLastResortRound(groupID int64, accounts []Account, model string, excluded, avoided map[int64]struct{}, boundID int64, now time.Time,
	include func(*Account) bool, penalty func(*Account, string, time.Time) (resilience.PenaltyTier, time.Time, string),
	gate func(acc *Account, sticky bool) sharesched.Reason) *shareLastResortRound {
	if penalty == nil {
		penalty = resiliencePenaltyOf
	}
	r := &shareLastResortRound{accounts: map[int64]*Account{}, recoverAt: map[int64]time.Time{}}
	for i := range accounts {
		acc := accounts[i]
		if include != nil && !include(&acc) {
			continue
		}
		c := s.shareCandidate(groupID, &acc, now)
		tier, recoverAt, why := penalty(&acc, model, now)
		_, isAvoided := avoided[acc.ID]
		if acc.ID == boundID && !isAvoided && tier != resilience.TierHard {
			r.keep = acc.ID
		}
		if acc.ID == boundID && isAvoided {
			r.rebind = true
		}
		switch _, failed := excluded[acc.ID]; {
		case failed:
			// Failed in this request: as in normal placement it is
			// excluded and only anchors the spill order.
			c.Excluded, c.Eligible = true, true
		case tier == resilience.TierHard:
			c.Reason = shareLastResortHardReason(&acc, why, now)
		case !lastResortTierAllowed(tier, acc.ID, avoided):
			// Healthy accounts the first round could not use stay out.
			c.Reason = sharesched.ReasonIneligible
		default:
			candidate := withoutSoftPenalties(acc, now)
			if c.Reason = gate(candidate, acc.ID == boundID); c.Reason == "" {
				c.Eligible = true
				// Read-only for eligible accounts: when the account last
				// failed a health gate, which defers tier return.
				c.StableSince = s.observeEligibility(acc.ID, true, "", now)
				r.accounts[acc.ID], r.recoverAt[acc.ID] = candidate, recoverAt
			} else if acc.ID == boundID {
				r.keep = 0
			}
		}
		r.cands = append(r.cands, c)
	}
	return r
}

// shareLastResortHardReason names the hard state that keeps an account out
// of the round.
func shareLastResortHardReason(a *Account, why string, now time.Time) sharesched.Reason {
	switch why {
	case "balance_exhausted":
		return sharesched.ReasonBalanceExhausted
	case "model_rate_limited":
		return sharesched.ReasonModelRateLimited
	}
	reason, _ := shareAccountStateReason(a, now)
	return reason
}

// plan runs the share plan over the round with the binding writes of last
// resort: a soft-penalised owner keeps the binding, a binding to an avoided
// account moves, and anything else follows the plan.
func (r *shareLastResortRound) plan(req sharesched.Request) sharesched.Decision {
	if r.rebind {
		req.Binding = sharesched.Binding{}
	}
	d := sharesched.Plan(req, r.cands)
	switch {
	case r.rebind:
		d.Write, d.Spill = sharesched.WriteReplace, sharesched.WriteReplace
	case r.keep > 0:
		if len(d.Steps) == 0 || d.Steps[0].AccountID != r.keep {
			d.Write = sharesched.WriteNone
		}
		d.Spill = sharesched.WriteNone
	}
	return d
}

// lastResortGroupCache keeps the group account list for a short time.
type lastResortGroupCache struct {
	mu      sync.Mutex
	entries map[int64]lastResortGroupEntry
}

type lastResortGroupEntry struct {
	accounts []Account
	expires  time.Time
}

// sweep forgets expired snapshots so an idle group does not keep its account
// copies (credentials and extra included) until the table is reset.
func (c *lastResortGroupCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
}

func (c *lastResortGroupCache) sweepLocked(now time.Time) {
	for id, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, id)
		}
	}
}

// drop forgets the cached accounts of a group; groupID <= 0 forgets all.
func (c *lastResortGroupCache) drop(groupID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if groupID <= 0 {
		c.entries = nil
		return
	}
	delete(c.entries, groupID)
}

func (r *ResilienceService) groupAccountsForLastResort(ctx context.Context, repo AccountRepository, groupID int64) ([]Account, error) {
	now := time.Now()
	if r != nil {
		r.lastResortGroups.mu.Lock()
		if e, ok := r.lastResortGroups.entries[groupID]; ok && now.Before(e.expires) {
			r.lastResortGroups.mu.Unlock()
			return e.accounts, nil
		}
		r.lastResortGroups.mu.Unlock()
	}
	accounts, err := repo.ListByGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if r != nil {
		r.lastResortGroups.mu.Lock()
		r.lastResortGroups.sweepLocked(now)
		if r.lastResortGroups.entries == nil || len(r.lastResortGroups.entries) > 1024 {
			r.lastResortGroups.entries = map[int64]lastResortGroupEntry{}
		}
		r.lastResortGroups.entries[groupID] = lastResortGroupEntry{accounts: accounts, expires: now.Add(lastResortCandidateTTL)}
		r.lastResortGroups.mu.Unlock()
	}
	return accounts, nil
}

// verifyLastResortAccount re-reads a planned last-resort account just before
// it is dispatched (review R09). Planning uses a group list cached for 2 s,
// which may predate an administrator's change (disabled, unschedulable,
// deleted, moved to another group) or a new hard pause. The account is
// served only when its current row still allows last resort; the returned
// copy is that row with soft penalties relaxed. A failed read keeps the
// planned copy (the list itself was read moments ago).
func (r *ResilienceService) verifyLastResortAccount(ctx context.Context, repo AccountRepository, groupID int64, planned *Account, model string,
	penaltyOf func(*Account, string, time.Time) (resilience.PenaltyTier, time.Time, string)) (*Account, bool) {
	if planned == nil {
		return nil, false
	}
	if repo == nil {
		return planned, true
	}
	fresh, err := repo.GetByID(ctx, planned.ID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			r.lastResortGroups.drop(groupID)
			return nil, false
		}
		slog.Warn("resilience.last_resort_verify_failed", "group_id", groupID, "account_id", planned.ID, "error", err)
		return planned, true
	}
	now := time.Now()
	if fresh == nil || (groupID > 0 && len(fresh.GroupIDs) > 0 && !containsInt64(fresh.GroupIDs, groupID)) {
		r.lastResortGroups.drop(groupID)
		return nil, false
	}
	if tier, _, _ := penaltyOf(fresh, model, now); tier == resilience.TierHard {
		r.lastResortGroups.drop(groupID)
		return nil, false
	}
	return withoutSoftPenalties(*fresh, now), true
}

// isNoAccountSelectionError reports whether selection failed only because
// no account was available (not a configuration restriction).
func isNoAccountSelectionError(err error) bool {
	if err == nil || !errors.Is(err, ErrNoAvailableAccounts) {
		return false
	}
	msg := err.Error()
	return !strings.Contains(msg, "channel pricing restriction") && !strings.Contains(msg, "composite target platform unknown")
}

// resilienceGatewayLastResort selects a soft-penalised account of the group
// when normal selection found none (design §4.3). It returns nil when no
// soft candidate exists. Share groups run the share plan's last-resort
// round; the original scheduler orders by priority, then by the end of the
// penalty.
func (s *GatewayService) resilienceGatewayLastResort(ctx context.Context, group *Group, groupID *int64, sessionHash, requestedModel string, excludedIDs, avoided map[int64]struct{}) *AccountSelectionResult {
	if s == nil || s.resilience == nil || groupID == nil || s.accountRepo == nil || !resilience.Current().LastResortOn() || ctx.Err() != nil {
		return nil
	}
	if gp := s.shareScheduling.groupPolicy(*groupID); gp != nil && gp.policy.Scheduler == sharesched.SchedulerShare {
		return s.shareGatewayLastResort(ctx, gp, group, groupID, sessionHash, requestedModel, excludedIDs, avoided)
	}
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
	// The candidates pass the same gates as in normal selection (window
	// cost and RPM included); only soft penalties are relaxed.
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)
	gates := s.newGatewayCandidateGates(ctx, groupID, platform, useMixed, requestedModel)
	gateCtx := withResilienceLastResortGate(ctx)
	owner := resilience.StateFrom(ctx).StickyOwner()
	now := time.Now()
	var cands []lastResortCandidate
	for i := range accounts {
		acc := accounts[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		tier, recoverAt, _ := resiliencePenaltyOf(&acc, requestedModel, now)
		if !lastResortTierAllowed(tier, acc.ID, avoided) {
			continue
		}
		candidate := withoutSoftPenalties(acc, now)
		if reason, _ := gates.candidateReason(gateCtx, candidate, acc.ID == owner, now); reason != "" {
			continue
		}
		cands = append(cands, lastResortCandidate{account: candidate, recoverAt: recoverAt})
	}
	// Take the first candidate with a free slot; when all are busy, wait
	// for the best one.
	var busy []lastResortCandidate
	for _, c := range pickLastResort(cands) {
		acc, ok := s.resilience.verifyLastResortAccount(ctx, s.accountRepo, *groupID, c.account, requestedModel, resiliencePenaltyOf)
		if !ok {
			continue
		}
		c.account = acc
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
		if err != nil || result == nil || !result.Acquired {
			busy = append(busy, c)
			continue
		}
		if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
			result.ReleaseFunc()
			resilience.StateFrom(ctx).MarkLastResortSessionLimited()
			continue
		}
		s.noteLastResort(ctx, groupID, acc, c.recoverAt)
		sel, err := s.newSelectionResult(ctx, acc, true, result.ReleaseFunc, nil)
		if err != nil {
			result.ReleaseFunc()
			return nil
		}
		return sel
	}
	cfg := s.schedulingConfig()
	for _, c := range busy {
		acc := c.account
		if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
			resilience.StateFrom(ctx).MarkLastResortSessionLimited()
			continue
		}
		s.noteLastResort(ctx, groupID, acc, c.recoverAt)
		sel, err := s.newSelectionResult(ctx, acc, false, nil, &AccountWaitPlan{
			AccountID:      acc.ID,
			MaxConcurrency: acc.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
		if err != nil {
			return nil
		}
		return sel
	}
	return nil
}

func (s *GatewayService) noteLastResort(ctx context.Context, groupID *int64, account *Account, recoverAt time.Time) {
	resilience.StateFrom(ctx).SetLastResort(account.ID)
	s.resilience.noteLastResortSelected(account.ID)
	s.shareScheduling.noteResilienceReason(derefGroupID(groupID), sharesched.ReasonLastResortSoft)
	resilience.Count(resilience.MetricLastResort, derefGroupID(groupID), account.ID, account.Platform)
	slog.Warn("resilience.last_resort", append(resilience.EventFields(ctx),
		"group_id", derefGroupID(groupID),
		"account_id", account.ID,
		"platform", account.Platform,
		"recover_at", recoverAt,
		"reason", string(sharesched.ReasonLastResortSoft),
	)...)
}

// noteLastResortSelected remembers that an account served as last resort so
// that a success lifts its transient cooldown early.
func (r *ResilienceService) noteLastResortSelected(accountID int64) {
	if r == nil {
		return
	}
	r.lastResortUsed.Store(accountID, time.Now())
}

// lastResortTierAllowed: soft-penalised accounts, and healthy accounts the
// session only avoids, may serve as a last resort; hard states never.
func lastResortTierAllowed(tier resilience.PenaltyTier, accountID int64, avoided map[int64]struct{}) bool {
	if tier == resilience.TierSoft {
		return true
	}
	_, isAvoided := avoided[accountID]
	return tier == resilience.TierNone && isAvoided
}

// resilienceGatewaySelectionEpilogue runs after gateway selection: it keeps a
// soft-penalised session binding when another account serves this request,
// turns "no available account" into a last-resort selection, attaches the
// session failure recorder and records the conversation for the failed-turn
// cap.
func (s *GatewayService) resilienceGatewaySelectionEpilogue(ctx context.Context, group *Group, groupID *int64, sessionHash, requestedModel string, excludedIDs, avoided map[int64]struct{}, result *AccountSelectionResult, err error) (*AccountSelectionResult, error) {
	markResilienceConfigError(ctx, err)
	resilience.StateFrom(ctx).NoteTurn(derefGroupID(groupID), requestedModel, sessionHash)
	if err != nil && isNoAccountSelectionError(err) && !errors.Is(err, errNegativeCached) {
		if lr := s.resilienceGatewayLastResort(ctx, group, groupID, sessionHash, requestedModel, excludedIDs, avoided); lr != nil {
			result, err = lr, nil
		} else {
			resilienceNegativeCachePut(ctx, groupID, requestedModel, "", excludedIDs, err)
			return result, err
		}
	}
	if err == nil && result != nil && result.Account != nil && sessionHash != "" {
		resilience.StateFrom(ctx).SetSession(derefGroupID(groupID), sessionHash, s.gatewaySessionRecorder())
	}
	return result, err
}

// bindGatewayStickySession receives the selection model explicitly because
// eager bindings precede the selection epilogue's NoteTurn.
func (s *GatewayService) bindGatewayStickySession(ctx context.Context, groupID *int64, sessionHash string, accountID int64, requestedModel string) error {
	sessionHash = monitorProbeSessionHash(ctx, sessionHash)
	if sessionHash == "" || accountID <= 0 || s.cache == nil {
		return nil
	}
	if !s.resilienceGatewayBindAllowed(ctx, accountID, requestedModel) {
		return nil
	}
	ttl := stickySessionTTL
	if shareTTL, ok := s.shareScheduling.shareBindingTTL(derefGroupID(groupID)); ok {
		ttl = shareTTL
	}
	return s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, accountID, ttl)
}

// resilienceGatewayBindAllowed reports whether the session may be bound to
// accountID. When the request started with a different owner that is only
// soft-penalised, the owner keeps the binding (design §4.2); the owner is
// looked up only in that mismatch case.
func (s *GatewayService) resilienceGatewayBindAllowed(ctx context.Context, accountID int64, requestedModel string) bool {
	st := resilience.StateFrom(ctx)
	if !resilienceBindAllowed(ctx, accountID) {
		return false
	}
	owner := st.StickyOwner()
	if owner <= 0 || owner == accountID || !resilience.Current().SessionOn() || st.Avoided(owner) {
		// An owner the session now avoids lost its binding to the streak.
		return true
	}
	bound, err := s.getSchedulableAccount(ctx, owner)
	if err != nil || bound == nil {
		return true
	}
	if resilienceKeepsSoftStickyBinding(bound, requestedModel) {
		st.KeepSticky(owner)
		return false
	}
	return true
}

// resilienceKeepOpenAISoftOwner keeps the binding of a soft-penalised owner
// on the original OpenAI scheduler (legacy and advanced paths), as the
// Gateway does (design §4.2): the request is served elsewhere and the
// session returns afterwards. The scheduler's per-model transient block
// counts as soft here, as it does for last resort. It reports whether the
// owner was kept.
func (s *OpenAIGatewayService) resilienceKeepOpenAISoftOwner(ctx context.Context, account *Account, platform, requestedModel string) bool {
	if account == nil || account.Platform != NormalizeOpenAICompatiblePlatform(platform) || !account.IsOpenAICompatible() ||
		!resilience.Current().SessionOn() {
		return false
	}
	if tier, _, _ := s.resilienceOpenAIPenaltyOf(account, requestedModel, time.Now()); tier != resilience.TierSoft {
		return false
	}
	resilience.StateFrom(ctx).KeepSticky(account.ID)
	return true
}

// resilienceBindAllowed reports whether a sticky binding to accountID may be
// written for this request: a binding kept for a soft-penalised owner must
// not be replaced by a temporary placement.
func resilienceBindAllowed(ctx context.Context, accountID int64) bool {
	st := resilience.StateFrom(ctx)
	kept := st.KeptSticky()
	return kept == 0 || kept == accountID || st.Avoided(kept)
}

// resilienceOpenAILastResort is the OpenAI-scheduler variant of last-resort
// selection. It reuses the scheduler's per-account request gate on a copy
// with the soft restrictions cleared. Requests that share scheduling places
// run the share plan's last-resort round instead.
func (s *OpenAIGatewayService) resilienceOpenAILastResort(ctx context.Context, req OpenAIAccountScheduleRequest, avoided map[int64]struct{}) *AccountSelectionResult {
	if s == nil || s.resilience == nil || req.GroupID == nil || s.accountRepo == nil || !resilience.Current().LastResortOn() || ctx.Err() != nil {
		return nil
	}
	if gp := s.shareScheduling.groupPolicy(*req.GroupID); gp != nil && gp.policy.Scheduler == sharesched.SchedulerShare &&
		!req.RequireCompact && req.RequiredImageCapability == "" && !s.qualityOwnsSticky(ctx, req.GroupID, req.SessionHash) {
		return s.shareOpenAILastResort(ctx, gp, req, avoided)
	}
	accounts, err := s.resilience.groupAccountsForLastResort(ctx, s.accountRepo, *req.GroupID)
	if err != nil {
		slog.Warn("resilience.last_resort_list_failed", "group_id", *req.GroupID, "error", err)
		return nil
	}
	platform := NormalizeOpenAICompatiblePlatform(req.Platform)
	gate := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	gateCtx := withResilienceLastResortGate(ctx)
	now := time.Now()
	var cands []lastResortCandidate
	for i := range accounts {
		acc := accounts[i]
		if _, excluded := req.ExcludedIDs[acc.ID]; excluded {
			continue
		}
		if acc.Platform != platform || !acc.IsOpenAICompatible() {
			continue
		}
		tier, recoverAt, _ := s.resilienceOpenAIPenaltyOf(&acc, req.RequestedModel, now)
		if !lastResortTierAllowed(tier, acc.ID, avoided) {
			continue
		}
		candidate := withoutSoftPenalties(acc, now)
		if reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(gateCtx, candidate, platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability); reason != "" &&
			(reason != "model_rate_limited" || !lastResortModelLimitSoft(gateCtx, candidate, req.RequestedModel)) {
			continue
		}
		if ok, _ := gate.isAccountRequestCompatibleReason(gateCtx, candidate, req); !ok {
			continue
		}
		if !gate.isAccountTransportCompatible(candidate, req.RequiredTransport) {
			continue
		}
		held := resilience.Current().Enabled && s.openAIAccountLatencyHoldSnapshot(candidate, canonicalOpenAIAccountSchedulingModel(candidate, req.RequestedModel), now).Held
		cands = append(cands, lastResortCandidate{account: candidate, recoverAt: recoverAt, held: held})
	}
	cands = pickLastResort(cands)
	if len(cands) == 0 {
		return nil
	}
	// Take the first candidate with a free slot; when all are busy, wait
	// for the best one. Codex accounts under upstream pressure take slots
	// only up to their adaptive limit.
	verified := cands[:0]
	for _, c := range cands {
		acc, ok := s.resilience.verifyLastResortAccount(ctx, s.accountRepo, *req.GroupID, c.account, req.RequestedModel, s.resilienceOpenAIPenaltyOf)
		if !ok {
			continue
		}
		c.account = acc
		verified = append(verified, c)
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, s.codexEffectiveConcurrency(ctx, acc.ID, acc.Concurrency))
		if err == nil && result != nil && result.Acquired {
			s.noteOpenAILastResort(ctx, req.GroupID, acc, c.recoverAt)
			return attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: acc, Acquired: true, ReleaseFunc: result.ReleaseFunc})
		}
	}
	if len(verified) == 0 {
		return nil
	}
	cfg := s.schedulingConfig()
	acc := verified[0].account
	s.noteOpenAILastResort(ctx, req.GroupID, acc, verified[0].recoverAt)
	return attachSelectionProfitGate(ctx, &AccountSelectionResult{
		Account: acc,
		WaitPlan: &AccountWaitPlan{
			AccountID:      acc.ID,
			MaxConcurrency: s.codexEffectiveConcurrency(ctx, acc.ID, acc.Concurrency),
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		},
	})
}

func (s *OpenAIGatewayService) noteOpenAILastResort(ctx context.Context, groupID *int64, account *Account, recoverAt time.Time) {
	resilience.StateFrom(ctx).SetLastResort(account.ID)
	s.resilience.noteLastResortSelected(account.ID)
	s.shareScheduling.noteResilienceReason(derefGroupID(groupID), sharesched.ReasonLastResortSoft)
	resilience.Count(resilience.MetricLastResort, derefGroupID(groupID), account.ID, account.Platform)
	slog.Warn("resilience.last_resort", append(resilience.EventFields(ctx),
		"group_id", derefGroupID(groupID),
		"account_id", account.ID,
		"platform", account.Platform,
		"recover_at", recoverAt,
		"reason", string(sharesched.ReasonLastResortSoft),
	)...)
}

type resilienceLastResortGateKey struct{}

// withResilienceLastResortGate marks a context in which soft runtime blocks
// (in-memory 429/transient blocks, proxy quarantine) do not veto a
// last-resort candidate.
func withResilienceLastResortGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, resilienceLastResortGateKey{}, true)
}

func resilienceLastResortGate(ctx context.Context) bool {
	v, _ := ctx.Value(resilienceLastResortGateKey{}).(bool)
	return v
}

// lastResortModelLimitSoft reports whether a per-model rate limit lets the
// account through as a last resort: only in a last-resort context and only
// while the limit is within the soft threshold (design §3.1).
func lastResortModelLimitSoft(ctx context.Context, acc *Account, model string) bool {
	if acc != nil && accountHasIQGate(acc) {
		for _, key := range iqGateKeysForRequest(acc, model, false) {
			if acc.hasRateLimitKey(key) {
				return false
			}
		}
	}
	return resilienceLastResortGate(ctx) && acc.GetRateLimitRemainingTimeWithContext(ctx, model) <= resilience.HardRateLimitThreshold
}

// sessionFailureRecorder implements resilience.SessionRecorder for one
// scheduler; deleteIfOwner removes a binding only while it still points to
// the failing account.
type sessionFailureRecorder struct {
	r             *ResilienceService
	deleteIfOwner func(ctx context.Context, groupID int64, sessionHash string, accountID int64) error
}

// Failure counts a failure of the session on an account. Reaching the streak
// threshold deletes the binding (owner-checked) and avoids the account for
// the session (design §4.1-§4.2).
func (rec *sessionFailureRecorder) Failure(ctx context.Context, groupID int64, sessionHash string, accountID int64, c resilience.Category) {
	cfg := resilience.Current()
	if rec == nil || rec.r == nil || rec.r.store == nil || !cfg.SessionOn() || !c.CountsForSession() {
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	window := time.Duration(cfg.Session.StreakWindowSeconds) * time.Second
	n, err := rec.r.store.IncrSessionFailure(stateCtx, groupID, sessionHash, accountID, window)
	if err != nil {
		return
	}
	rec.r.markSessionFailures(groupID, sessionHash, window)
	if n < int64(cfg.Session.StreakThreshold) {
		return
	}
	if rec.deleteIfOwner != nil {
		_ = rec.deleteIfOwner(stateCtx, groupID, sessionHash, accountID)
	}
	avoid := time.Duration(cfg.Session.AvoidSeconds) * time.Second
	_ = rec.r.store.AddSessionAvoid(stateCtx, groupID, sessionHash, accountID, avoid)
	rec.r.avoidingSessions.Store(sessionMarkKey(groupID, sessionHash), time.Now().Add(avoid))
	// The request that hit the streak must not keep or restore the binding.
	resilience.StateFrom(ctx).AddAvoided(accountID)
	_ = rec.r.store.ClearSessionFailure(stateCtx, groupID, sessionHash, accountID)
	resilience.Count(resilience.MetricSessionRebind, groupID, accountID, "resil_failure_streak")
	slog.Warn("resilience.session_rebind", append(resilience.EventFields(ctx),
		"group_id", groupID,
		"session", shortSessionHash(sessionHash),
		"account_id", accountID,
		"failures", n,
		"category", string(c),
		"avoid", avoid.String(),
		"reason", "resil_failure_streak",
	)...)
}

// Success resets the session's failure count of the account. Only sessions
// this instance saw failing touch Redis, so the common path costs nothing.
func (rec *sessionFailureRecorder) Success(ctx context.Context, groupID int64, sessionHash string, accountID int64) {
	if rec == nil || rec.r == nil || rec.r.store == nil || !rec.r.sessionHadFailures(groupID, sessionHash) {
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	_ = rec.r.store.ClearSessionFailure(stateCtx, groupID, sessionHash, accountID)
}

func sessionMarkKey(groupID int64, sessionHash string) string {
	return sessionHash + "\x00" + strconv.FormatInt(groupID, 10)
}

func (r *ResilienceService) markSessionFailures(groupID int64, sessionHash string, window time.Duration) {
	if r == nil {
		return
	}
	r.failingSessions.Store(sessionMarkKey(groupID, sessionHash), time.Now().Add(window))
}

func (r *ResilienceService) sessionMarkLoop(ctx context.Context) {
	defer r.wg.Done()
	t := time.NewTicker(resilienceSessionMarkSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.sweepSessionMarks(now)
		}
	}
}

// sweepSessionMarks drops expired failure and avoid marks; without it every
// session that failed once and never returned would stay in memory. The
// per-account counts of failures after output and the expired last-resort
// group snapshots go on the same tick.
func (r *ResilienceService) sweepSessionMarks(now time.Time) {
	r.postOutput.sweep(now)
	r.lastResortGroups.sweep(now)
	for _, m := range []*sync.Map{&r.failingSessions, &r.avoidingSessions} {
		m.Range(func(k, v any) bool {
			if until, ok := v.(time.Time); !ok || now.After(until) {
				m.Delete(k)
			}
			return true
		})
	}
}

func (r *ResilienceService) sessionHadFailures(groupID int64, sessionHash string) bool {
	if r == nil {
		return false
	}
	key := sessionMarkKey(groupID, sessionHash)
	v, ok := r.failingSessions.Load(key)
	if !ok {
		return false
	}
	if until, _ := v.(time.Time); time.Now().After(until) {
		r.failingSessions.Delete(key)
		return false
	}
	return true
}

// gatewaySessionRecorder returns the recorder of the gateway scheduler.
func (s *GatewayService) gatewaySessionRecorder() resilience.SessionRecorder {
	if s == nil || s.resilience == nil || s.cache == nil {
		return nil
	}
	return &sessionFailureRecorder{r: s.resilience, deleteIfOwner: func(ctx context.Context, groupID int64, sessionHash string, accountID int64) error {
		if atomic, ok := s.cache.(OpenAIStickyAtomicCache); ok {
			return atomic.DeleteSessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
		}
		current, err := s.cache.GetSessionAccountID(ctx, groupID, sessionHash)
		if err != nil || current != accountID {
			return err
		}
		return s.cache.DeleteSessionAccountID(ctx, groupID, sessionHash)
	}}
}

// openAISessionRecorder returns the recorder of the OpenAI scheduler.
func (s *OpenAIGatewayService) openAISessionRecorder() resilience.SessionRecorder {
	if s == nil || s.resilience == nil {
		return nil
	}
	return &sessionFailureRecorder{r: s.resilience, deleteIfOwner: func(ctx context.Context, groupID int64, sessionHash string, accountID int64) error {
		gid := groupID
		return s.deleteStickySessionAccountIDIfOwner(ctx, &gid, sessionHash, accountID)
	}}
}

// sessionAvoid returns the accounts the session currently avoids (nil when
// none or when session handling is off).
func (r *ResilienceService) sessionAvoid(ctx context.Context, groupID *int64, sessionHash string) map[int64]struct{} {
	if r == nil || r.store == nil || sessionHash == "" || !resilience.Current().SessionOn() {
		return nil
	}
	// Only sessions that hit a failure streak on this instance carry avoid
	// entries worth a Redis read; every other request skips it.
	if !r.sessionMayAvoid(derefGroupID(groupID), sessionHash) {
		return nil
	}
	avoid, err := r.store.SessionAvoid(ctx, derefGroupID(groupID), sessionHash)
	if err != nil || len(avoid) == 0 {
		return nil
	}
	resilience.StateFrom(ctx).SetAvoided(avoid)
	return avoid
}

// mergeExcluded returns base plus extra without modifying either.
func mergeExcluded(base, extra map[int64]struct{}) map[int64]struct{} {
	if len(extra) == 0 {
		return base
	}
	out := make(map[int64]struct{}, len(base)+len(extra))
	for k := range base {
		out[k] = struct{}{}
	}
	for k := range extra {
		out[k] = struct{}{}
	}
	return out
}

func (r *ResilienceService) sessionMayAvoid(groupID int64, sessionHash string) bool {
	key := sessionMarkKey(groupID, sessionHash)
	v, ok := r.avoidingSessions.Load(key)
	if !ok {
		return false
	}
	if until, _ := v.(time.Time); time.Now().After(until) {
		r.avoidingSessions.Delete(key)
		return false
	}
	return true
}
