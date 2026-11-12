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
	if model != "" && iqGateBlocksRequest(a, model, false) {
		return resilience.TierHard, time.Time{}, "model_rate_limited"
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
		if a.modelUnsupportedRateLimitActive(context.Background(), model, now) {
			return hard(now.Add(a.GetRateLimitRemainingTimeWithContext(context.Background(), model)), "model_unsupported")
		}
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
// ticket gates are no penalty and stay gates. Same-site transport holds are
// also soft penalties, so an otherwise healthy held account remains eligible
// for last resort.
func (s *OpenAIGatewayService) resilienceOpenAIPenaltyOf(acc *Account, model string, now time.Time) (resilience.PenaltyTier, time.Time, string) {
	tier, recoverAt, why := resiliencePenaltyOf(acc, model, now)
	if tier == resilience.TierHard {
		return tier, recoverAt, why
	}
	if end := s.openAIModelBreakerEnd(acc, model, now); end.After(recoverAt) {
		tier, recoverAt, why = resilience.TierSoft, end, "model_transient"
	}
	if s != nil && s.resilience != nil {
		if end := s.resilience.sameSiteHold.until(acc, now); end.After(recoverAt) {
			tier, recoverAt, why = resilience.TierSoft, end, "same_site_hold"
		}
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
	avoided   bool
}

type lastResortLogMeta struct {
	candidates int
	reasons    map[string]int
}

func (m *lastResortLogMeta) addReason(reason string) {
	if reason == "" {
		return
	}
	if m.reasons == nil {
		m.reasons = make(map[string]int)
	}
	m.reasons[reason]++
}

func (m lastResortLogMeta) penaltyReason() string {
	if len(m.reasons) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m.reasons))
	for reason := range m.reasons {
		keys = append(keys, reason)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, reason := range keys {
		parts = append(parts, reason+":"+strconv.Itoa(m.reasons[reason]))
	}
	return strings.Join(parts, ",")
}

// pickLastResort tries accounts the session does not avoid first. Within
// each group it keeps priority (lower first), latency hold preference and
// the earliest end of the penalty (design §4.3).
func pickLastResort(cands []lastResortCandidate) []lastResortCandidate {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].avoided != cands[j].avoided {
			return !cands[i].avoided
		}
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
func (r *ResilienceService) verifyLastResortAccount(ctx context.Context, repo AccountRepository, groupID int64, planned *Account, model string, requireCompact bool,
	penaltyOf func(*Account, string, time.Time) (resilience.PenaltyTier, time.Time, string), compactOutboundForFresh func(*Account) string) (*Account, bool) {
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
	compactOutbound := ""
	if requireCompact && compactOutboundForFresh != nil {
		compactOutbound = compactOutboundForFresh(fresh)
	}
	if iqGateBlocksRequest(fresh, model, requireCompact, compactOutbound) {
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
	meta := lastResortLogMeta{candidates: len(accounts)}
	for i := range accounts {
		acc := accounts[i]
		_, isAvoided := avoided[acc.ID]
		if isAvoided {
			meta.addReason("session_avoid")
		}
		if _, excluded := excludedIDs[acc.ID]; excluded {
			if !isAvoided {
				meta.addReason("request_excluded")
			}
			continue
		}
		tier, recoverAt, penaltyReason := resiliencePenaltyOf(&acc, requestedModel, now)
		meta.addReason(penaltyReason)
		if !lastResortTierAllowed(tier, acc.ID, avoided) {
			if tier == resilience.TierNone {
				meta.addReason("tier_none")
			}
			continue
		}
		candidate := withoutSoftPenalties(acc, now)
		if reason, _ := gates.candidateReason(gateCtx, candidate, acc.ID == owner, now); reason != "" {
			meta.addReason(string(reason))
			continue
		}
		held := platform == PlatformAnthropic && s.anthropicLatencyHoldSnapshot(&acc, anthropicLatencyModel(ctx, requestedModel), now).Held
		cands = append(cands, lastResortCandidate{account: candidate, recoverAt: recoverAt, held: held, avoided: isAvoided})
	}
	// Take the first candidate with a free slot; when all are busy, wait
	// for the best one.
	var busy []lastResortCandidate
	for _, c := range pickLastResort(cands) {
		acc, ok := s.resilience.verifyLastResortAccount(ctx, s.accountRepo, *groupID, c.account, requestedModel, false, resiliencePenaltyOf, nil)
		if !ok {
			meta.addReason("verify_failed")
			continue
		}
		c.account = acc
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
		if err != nil || result == nil || !result.Acquired {
			meta.addReason("capacity")
			busy = append(busy, c)
			continue
		}
		if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
			meta.addReason("session_limit")
			result.ReleaseFunc()
			resilience.StateFrom(ctx).MarkLastResortSessionLimited()
			continue
		}
		s.noteLastResort(ctx, groupID, acc, c.recoverAt, avoided, meta)
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
			meta.addReason("session_limit")
			resilience.StateFrom(ctx).MarkLastResortSessionLimited()
			continue
		}
		s.noteLastResort(ctx, groupID, acc, c.recoverAt, avoided, meta)
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

func (s *GatewayService) noteLastResort(ctx context.Context, groupID *int64, account *Account, recoverAt time.Time, avoided map[int64]struct{}, meta lastResortLogMeta) {
	resilience.StateFrom(ctx).SetLastResort(account.ID)
	s.resilience.noteLastResortSelected(account.ID)
	resilience.Count(resilience.MetricLastResort, derefGroupID(groupID), account.ID, account.Platform)
	slog.Warn("resilience.last_resort", append(resilience.EventFields(ctx),
		"group_id", derefGroupID(groupID),
		"account_id", account.ID,
		"platform", account.Platform,
		"recover_at", recoverAt,
		"reason", "last_resort_soft",
		"candidates", meta.candidates,
		"penalty_reason", meta.penaltyReason(),
	)...)
	if _, sessionAvoided := avoided[account.ID]; sessionAvoided {
		slog.Warn("resilience.last_resort_avoided", append(resilience.EventFields(ctx),
			"group_id", derefGroupID(groupID),
			"account_id", account.ID,
			"reason", "session_avoid",
			"candidates", meta.candidates,
			"penalty_reason", meta.penaltyReason(),
		)...)
	}
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
// counts as soft here, as it does for last resort. Same-site holds instead
// let the normal runtime gate delete the owner and rebind the session.
// It reports whether the owner was kept.
func (s *OpenAIGatewayService) resilienceKeepOpenAISoftOwner(ctx context.Context, account *Account, platform, requestedModel string) bool {
	if account == nil || account.Platform != NormalizeOpenAICompatiblePlatform(platform) || !account.IsOpenAICompatible() ||
		!resilience.Current().SessionOn() {
		return false
	}
	now := time.Now()
	if s != nil && s.resilience != nil && s.resilience.sameSiteHold.held(account, now) {
		return false
	}
	if tier, _, _ := s.resilienceOpenAIPenaltyOf(account, requestedModel, now); tier != resilience.TierSoft {
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
func lastResortModelLimitSoft(ctx context.Context, acc *Account, model string, requireCompact bool, compactOutbound ...string) bool {
	if iqGateBlocksRequest(acc, model, requireCompact, compactOutbound...) ||
		acc.modelUnsupportedRateLimitActive(ctx, model, time.Now()) {
		return false
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

// AvoidOpenAIStateAccount records a request-scoped state repair avoidance
// without incrementing the account failure streak or health counters.
func (s *OpenAIGatewayService) AvoidOpenAIStateAccount(ctx context.Context, groupID int64, sessionHash string, accountID int64) {
	if s == nil || s.resilience == nil || s.resilience.store == nil || sessionHash == "" || accountID <= 0 {
		resilience.StateFrom(ctx).AddAvoided(accountID)
		return
	}
	avoid := time.Duration(resilience.Current().Session.AvoidSeconds) * time.Second
	if avoid <= 0 {
		avoid = 10 * time.Minute
	}
	_ = s.resilience.store.AddSessionAvoid(ctx, groupID, sessionHash, accountID, avoid)
	s.resilience.avoidingSessions.Store(sessionMarkKey(groupID, sessionHash), time.Now().Add(avoid))
	_ = s.deleteStickySessionAccountIDIfOwner(ctx, &groupID, sessionHash, accountID)
	resilience.StateFrom(ctx).AddAvoided(accountID)
}

// Failure counts a failure of the session on an account. Reaching the streak
// threshold, or a P1-B post-output signal, deletes the binding (owner-checked)
// and avoids the account for the session (design §4.1-§4.2).
func (rec *sessionFailureRecorder) Failure(ctx context.Context, groupID int64, sessionHash string, accountID int64, c resilience.Category) {
	if rec != nil && rec.r != nil {
		rec.r.terminalRejections.reset(groupID, openAITerminalRejectionSession(ctx, sessionHash), accountID)
	}
	cfg := resilience.Current()
	if rec == nil || rec.r == nil || rec.r.store == nil || !cfg.SessionOn() || !c.CountsForSession() {
		return
	}
	postOutput := cfg.PostOutputRebindOn() && resilience.IsPostOutputSessionFailure(ctx)
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	window := time.Duration(cfg.Session.StreakWindowSeconds) * time.Second
	n, err := rec.r.store.IncrSessionFailure(stateCtx, groupID, sessionHash, accountID, window)
	if err != nil {
		return
	}
	rec.r.markSessionFailures(groupID, sessionHash, window)
	if !postOutput && n < int64(cfg.Session.StreakThreshold) {
		return
	}
	reason := "resil_failure_streak"
	if postOutput {
		reason = "post_output_first_failure"
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
	resilience.Count(resilience.MetricSessionRebind, groupID, accountID, reason)
	slog.Warn("resilience.session_rebind", append(resilience.EventFields(ctx),
		"group_id", groupID,
		"session", shortSessionHash(sessionHash),
		"account_id", accountID,
		"failures", n,
		"category", string(c),
		"avoid", avoid.String(),
		"reason", reason,
	)...)
}

// Success resets the session's failure count of the account. Only sessions
// this instance saw failing touch Redis, so the common path costs nothing.
func (rec *sessionFailureRecorder) Success(ctx context.Context, groupID int64, sessionHash string, accountID int64) {
	if rec != nil && rec.r != nil {
		rec.r.terminalRejections.reset(groupID, openAITerminalRejectionSession(ctx, sessionHash), accountID)
	}
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
