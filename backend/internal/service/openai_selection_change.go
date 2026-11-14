package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const openAIStickyChangeAvoidTTL = 2 * time.Minute

// The existing resilience avoid set permits last-resort reuse. Failed change
// targets have a strict two-minute exclusion, so they use a separate scope in
// the same store, without changing the older failure-streak behavior.
func stickyChangeAvoidSession(session string) string {
	return "sticky_change:" + strings.TrimSpace(session)
}

func (s *OpenAIGatewayService) stickyChangeAvoid(ctx context.Context, groupID *int64, session string) map[int64]struct{} {
	if s == nil || s.resilience == nil || s.resilience.store == nil || session == "" {
		return nil
	}
	avoided, err := sessionAvoidAfterReset(ctx, s.resilience.store, derefGroupID(groupID), stickyChangeAvoidSession(session), s.resilience.sessionResetAt)
	if err != nil {
		return nil
	}
	return avoided
}

// A selection only plans a change. Admission and billing must never commit it.
type openAIStickyChange struct {
	mu               sync.Mutex
	done             bool
	attemptSucceeded bool
	attemptCanceled  bool
	activeAttempt    *schedAttemptCapture
	groupID          int64
	session          string
	key              string
	model            string
	kind             string
	expected         StickySessionBinding
	target           int64
	ttl              time.Duration
	cache            OpenAIStickyVersionedCache
	dualWriteLegacy  bool
	skipCommit       bool // The snapshot read failed; keep all selection writes suppressed.
	// ownerReleased: selection hit a branch that deletes the owner's binding
	// (hard unavailable, failure streak). Only then may an ordinary placement
	// replace an existing owner (§5.2); other bypasses serve the request elsewhere.
	ownerReleased bool
	resetAt       func(int64) time.Time
}

func noteOpenAIStickyAttemptOutcome(attempt *schedAttemptCapture, success bool) {
	change := pendingOpenAIStickyChange(attempt.ctx)
	if change == nil || change.target != attempt.accountID {
		return
	}
	change.mu.Lock()
	if change.activeAttempt == attempt {
		change.attemptSucceeded = success
		change.attemptCanceled = attempt.client || attempt.state.ClientCanceled()
	}
	change.mu.Unlock()
}

// FinishOpenAIStickyForward runs after the complete forwarding call returns.
// A send may finish before buffered delivery or outer error conversion does.
func FinishOpenAIStickyForward(ctx context.Context, accountID int64, result *OpenAIForwardResult, err error) {
	change := pendingOpenAIStickyChange(ctx)
	if change == nil || change.target != accountID {
		return
	}
	change.mu.Lock()
	succeeded, attemptCanceled := change.attemptSucceeded, change.attemptCanceled
	change.mu.Unlock()
	canceled := attemptCanceled || resilience.StateFrom(ctx).ClientCanceled() || (result != nil && result.ClientDisconnect)
	success := succeeded && err == nil && ctx.Err() == nil && !canceled && result.CompletedForDelivery()
	CompleteOpenAIStickyChange(ctx, accountID, success, canceled)
}

func pendingOpenAIStickyChange(ctx context.Context) *openAIStickyChange {
	change, _ := resilience.StateFrom(ctx).StickyChange().(*openAIStickyChange)
	return change
}

func preservePendingOpenAIStickyBinding(ctx context.Context, groupID *int64, session string) bool {
	change := pendingOpenAIStickyChange(ctx)
	return change != nil && change.groupID == derefGroupID(groupID) && change.session == strings.TrimSpace(session)
}

// releasePendingOpenAIStickyOwner keeps the binding while a change is pending
// and records that the eager path would have deleted the owner's binding.
func releasePendingOpenAIStickyOwner(ctx context.Context, groupID *int64, session string, owner int64) bool {
	if !preservePendingOpenAIStickyBinding(ctx, groupID, session) {
		return false
	}
	change := pendingOpenAIStickyChange(ctx)
	change.mu.Lock()
	if owner == change.expected.AccountID {
		change.ownerReleased = true
	}
	change.mu.Unlock()
	return true
}

// openAIStickyBindingKept reports whether this request must not move the
// session to target: a kept soft owner, a guardian parent session, or a child
// thread's own rejection. The eager path skipped binding writes in these cases.
func openAIStickyBindingKept(ctx context.Context, session string, target int64) bool {
	state := resilience.StateFrom(ctx)
	return !resilienceBindAllowed(ctx, target) || state.LastResort() == target ||
		preserveOpenAIGuardianParentBinding(ctx, session) || preserveOpenAIRejectionBinding(ctx, session)
}

// Capture before selection can clear an unavailable owner or eagerly bind its
// replacement. A capacity spillover still borrows without changing ownership.
func (s *OpenAIGatewayService) prepareOpenAIStickyChange(ctx context.Context, groupID *int64, session string) {
	if resilience.StateFrom(ctx) == nil || s == nil || s.resilience == nil || s.resilience.store == nil || session == "" || s.qualityOwnsSticky(ctx, groupID, session) {
		return
	}
	if existing := pendingOpenAIStickyChange(ctx); existing != nil && existing.groupID == derefGroupID(groupID) && existing.session == strings.TrimSpace(session) {
		return
	}
	cache, ok := s.cache.(OpenAIStickyVersionedCache)
	if !ok {
		return
	}
	key := s.openAISessionCacheKey(session)
	expected, err := cache.GetSessionBinding(ctx, derefGroupID(groupID), key)
	if aliases, ok := s.cache.(OpenAIStickyAliasCache); ok {
		if legacy := s.openAILegacySessionCacheKey(ctx, session); legacy != "" && (s.openAISessionHashReadOldFallbackEnabled() || s.openAISessionHashDualWriteOldEnabled()) {
			expected, err = aliases.ResolveSessionBinding(ctx, derefGroupID(groupID), key, legacy, s.openAISessionHashReadOldFallbackEnabled())
		}
	} else if (err == nil && expected.AccountID <= 0 || errors.Is(err, ErrStickySessionNotFound)) && s.openAISessionHashReadOldFallbackEnabled() {
		if legacyKey := s.openAILegacySessionCacheKey(ctx, session); legacyKey != "" {
			key = legacyKey
			expected, err = cache.GetSessionBinding(ctx, derefGroupID(groupID), key)
		}
	}
	unreadable := err != nil && !errors.Is(err, ErrStickySessionNotFound)
	kind := "migrate"
	if expected.AccountID <= 0 {
		kind = "create"
		key = s.openAISessionCacheKey(session)
		expected = StickySessionBinding{LegacyKey: s.openAILegacySessionCacheKey(ctx, session)}
	} else if openAINoStickyMigration(ctx) {
		return
	}
	resilience.StateFrom(ctx).SetStickyChange(&openAIStickyChange{
		groupID: derefGroupID(groupID), session: strings.TrimSpace(session), key: key,
		kind: kind, expected: expected, ttl: s.openAIStickyBindingTTL(groupID), cache: cache,
		dualWriteLegacy: s.openAISessionHashDualWriteOldEnabled(),
		skipCommit:      unreadable,
		resetAt:         s.resilience.sessionAccountResetAt,
	})
}

func planOpenAIStickyReplacement(ctx context.Context, groupID *int64, session, model string, selection *AccountSelectionResult) {
	change := pendingOpenAIStickyChange(ctx)
	if change == nil || change.groupID != derefGroupID(groupID) || change.session != strings.TrimSpace(session) || selection == nil || selection.Account == nil || selection.StickyCapacitySpillover || selection.Account.ID == change.expected.AccountID {
		return
	}
	change.mu.Lock()
	defer change.mu.Unlock()
	if change.target == selection.Account.ID && !change.done {
		return
	}
	if change.expected.AccountID != 0 && !change.ownerReleased || openAIStickyBindingKept(ctx, change.session, selection.Account.ID) {
		return
	}
	kind := "migrate"
	if change.expected.AccountID == 0 {
		kind = "create"
	}
	resilience.StateFrom(ctx).SetStickyChange(&openAIStickyChange{
		groupID: change.groupID, session: change.session, key: change.key, model: model,
		kind: kind, expected: change.expected, target: selection.Account.ID, ttl: change.ttl, cache: change.cache,
		dualWriteLegacy: change.dualWriteLegacy,
		skipCommit:      change.skipCommit,
		ownerReleased:   change.ownerReleased,
		resetAt:         change.resetAt,
	})
}

// CompleteOpenAIStickyChange must be called by the actual attempt terminal path,
// including timeouts and unfinished streams. User cancellation keeps the old
// owner without penalizing the target. Repeated completion calls are harmless.
func CompleteOpenAIStickyChange(ctx context.Context, accountID int64, success, canceled bool) {
	change := pendingOpenAIStickyChange(ctx)
	if change == nil || change.target != accountID {
		return
	}
	change.mu.Lock()
	defer change.mu.Unlock()
	if change.done {
		return
	}
	change.done = true
	if canceled || change.skipCommit {
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if !success {
		if change.expected.AccountID == 0 {
			return
		}
		resilience.StateFrom(ctx).AddAvoided(accountID)
		var reset time.Time
		if change.resetAt != nil {
			reset = change.resetAt(accountID)
		}
		if _, err := change.cache.RecordSessionBindingFailure(stateCtx, change.groupID, change.key, stickyChangeAvoidSession(change.session), change.expected, accountID, openAIStickyChangeAvoidTTL, reset); err != nil {
			slog.WarnContext(ctx, "sched_quality.takeover_avoid_failed", "account_id", accountID, "error", err)
		}
		return
	}
	if openAIStickyBindingKept(ctx, change.session, change.target) {
		return
	}
	changed, err := change.cache.CommitSessionBinding(stateCtx, change.groupID, change.key, change.expected, change.target, change.ttl)
	if err != nil {
		slog.WarnContext(ctx, "sched_quality.takeover_commit_failed", "account_id", accountID, "error", err)
		return
	}
	if !changed {
		return
	}
	if change.dualWriteLegacy && change.expected.LegacyKey != "" {
		if aliases, ok := change.cache.(OpenAIStickyAliasCache); ok {
			if err := aliases.SyncSessionBindingAlias(stateCtx, change.groupID, change.key, change.expected.LegacyKey, accountID, min(change.ttl, 10*time.Minute), true); err != nil {
				slog.WarnContext(ctx, "sched_quality.binding_alias_failed", "account_id", accountID, "error", err)
			}
		}
	}
	st := resilience.StateFrom(ctx)
	st.ClearKeptStickyIfOwner(change.expected.AccountID)
	st.NoteStickyBypassBindingChange()
	if change.kind == "create" {
		return
	}
	slog.InfoContext(ctx, "sched_quality.takeover", "component", "sched_quality",
		"kind", change.kind, "gateway_request_id", st.GatewayRequestID(),
		"group_id", change.groupID, "model", change.model,
		"from_account_id", change.expected.AccountID, "to_account_id", change.target)
}
