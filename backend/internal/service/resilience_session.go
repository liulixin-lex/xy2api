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
// resort. recoverAt is when the latest soft restriction ends.
func resiliencePenaltyOf(a *Account, model string, now time.Time) (resilience.PenaltyTier, time.Time, string) {
	if a == nil || !a.IsActive() || !a.Schedulable || a.IQCheck.BlocksScheduling() {
		return resilience.TierHard, time.Time{}, "unavailable"
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return resilience.TierHard, time.Time{}, "expired"
	}
	if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
		return resilience.TierHard, time.Time{}, "quota_exceeded"
	}
	// The exclusion ends at the next check plus a grace period, after which
	// real traffic may re-check a stopped probe; the tier follows it.
	if resilience.BalanceStateOf(a.Extra) == resilience.BalanceExhausted {
		if rec := resilience.BalanceRecordFrom(a.Extra); rec.State == resilience.BalanceExhausted && now.Before(rec.ExclusionUntil(resilience.Current())) {
			return resilience.TierHard, time.Time{}, "balance_exhausted"
		}
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
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) {
		if resilience.TempUnschedTier(a.TempUnschedulableReason) == resilience.TierHard {
			return resilience.TierHard, time.Time{}, "temp_unschedulable"
		}
		soft(*a.TempUnschedulableUntil, "temp_unschedulable")
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
		if a.RateLimitResetAt.Sub(now) > resilience.HardRateLimitThreshold {
			return resilience.TierHard, time.Time{}, "rate_limited"
		}
		soft(*a.RateLimitResetAt, "rate_limited")
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		soft(*a.OverloadUntil, "overloaded")
	}
	if model != "" {
		if remaining := a.GetRateLimitRemainingTimeWithContext(context.Background(), model); remaining > 0 {
			if remaining > resilience.HardRateLimitThreshold {
				return resilience.TierHard, time.Time{}, "model_rate_limited"
			}
			soft(now.Add(remaining), "model_rate_limited")
		}
	}
	return tier, recoverAt, reason
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
}

// pickLastResort orders candidates by priority (lower first), then by the
// earliest end of the penalty (design §4.3).
func pickLastResort(cands []lastResortCandidate) []lastResortCandidate {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].account.Priority != cands[j].account.Priority {
			return cands[i].account.Priority < cands[j].account.Priority
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
		if r.lastResortGroups.entries == nil || len(r.lastResortGroups.entries) > 1024 {
			r.lastResortGroups.entries = map[int64]lastResortGroupEntry{}
		}
		r.lastResortGroups.entries[groupID] = lastResortGroupEntry{accounts: accounts, expires: now.Add(lastResortCandidateTTL)}
		r.lastResortGroups.mu.Unlock()
	}
	return accounts, nil
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
// soft candidate exists.
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
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	isChannelRestricted := func(account *Account) bool {
		return needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel)
	}
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
		if !s.isAccountAllowedForPlatform(candidate, platform, useMixed) ||
			(requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, candidate, requestedModel)) ||
			isChannelRestricted(candidate) || !s.isGatewayAccountProfitEligible(ctx, candidate) ||
			!s.isAccountSchedulableForQuota(candidate) {
			continue
		}
		cands = append(cands, lastResortCandidate{account: candidate, recoverAt: recoverAt})
	}
	cfg := s.schedulingConfig()
	for _, c := range pickLastResort(cands) {
		acc := c.account
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
		if err == nil && result != nil && result.Acquired {
			if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
				result.ReleaseFunc()
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
		if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
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
	slog.Warn("resilience.last_resort",
		"group_id", derefGroupID(groupID),
		"account_id", account.ID,
		"platform", account.Platform,
		"recover_at", recoverAt,
		"reason", string(sharesched.ReasonLastResortSoft),
	)
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

// resilienceGatewayBindAllowed reports whether the session may be bound to
// accountID. When the request started with a different owner that is only
// soft-penalised, the owner keeps the binding (design §4.2); the owner is
// looked up only in that mismatch case.
func (s *GatewayService) resilienceGatewayBindAllowed(ctx context.Context, accountID int64) bool {
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
	if resilienceKeepsSoftStickyBinding(bound, "") {
		st.KeepSticky(owner)
		return false
	}
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
// with the soft restrictions cleared.
func (s *OpenAIGatewayService) resilienceOpenAILastResort(ctx context.Context, req OpenAIAccountScheduleRequest, avoided map[int64]struct{}) *AccountSelectionResult {
	if s == nil || s.resilience == nil || req.GroupID == nil || s.accountRepo == nil || !resilience.Current().LastResortOn() || ctx.Err() != nil {
		return nil
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
		tier, recoverAt, _ := resiliencePenaltyOf(&acc, req.RequestedModel, now)
		if !lastResortTierAllowed(tier, acc.ID, avoided) {
			continue
		}
		candidate := withoutSoftPenalties(acc, now)
		if ok, _ := gate.isAccountRequestCompatibleReason(gateCtx, candidate, req); !ok {
			continue
		}
		if !gate.isAccountTransportCompatible(candidate, req.RequiredTransport) {
			continue
		}
		cands = append(cands, lastResortCandidate{account: candidate, recoverAt: recoverAt})
	}
	cfg := s.schedulingConfig()
	for _, c := range pickLastResort(cands) {
		acc := c.account
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
		s.noteOpenAILastResort(ctx, req.GroupID, acc, c.recoverAt)
		if err == nil && result != nil && result.Acquired {
			return attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: acc, Acquired: true, ReleaseFunc: result.ReleaseFunc})
		}
		return attachSelectionProfitGate(ctx, &AccountSelectionResult{
			Account: acc,
			WaitPlan: &AccountWaitPlan{
				AccountID:      acc.ID,
				MaxConcurrency: acc.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		})
	}
	return nil
}

func (s *OpenAIGatewayService) noteOpenAILastResort(ctx context.Context, groupID *int64, account *Account, recoverAt time.Time) {
	resilience.StateFrom(ctx).SetLastResort(account.ID)
	s.resilience.noteLastResortSelected(account.ID)
	s.shareScheduling.noteResilienceReason(derefGroupID(groupID), sharesched.ReasonLastResortSoft)
	resilience.Count(resilience.MetricLastResort, derefGroupID(groupID), account.ID, account.Platform)
	slog.Warn("resilience.last_resort",
		"group_id", derefGroupID(groupID),
		"account_id", account.ID,
		"platform", account.Platform,
		"recover_at", recoverAt,
		"reason", string(sharesched.ReasonLastResortSoft),
	)
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
	slog.Warn("resilience.session_rebind",
		"group_id", groupID,
		"session", shortSessionHash(sessionHash),
		"account_id", accountID,
		"failures", n,
		"category", string(c),
		"avoid", avoid.String(),
		"reason", "resil_failure_streak",
	)
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
// session that failed once and never returned would stay in memory.
func (r *ResilienceService) sweepSessionMarks(now time.Time) {
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
