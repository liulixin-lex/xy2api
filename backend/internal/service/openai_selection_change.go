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
	avoided, err := s.resilience.store.SessionAvoid(ctx, derefGroupID(groupID), stickyChangeAvoidSession(session))
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
}

func noteOpenAIStickyAttemptOutcome(attempt *schedAttemptCapture, success bool) {
	change := pendingOpenAIStickyChange(attempt.ctx)
	if change == nil || change.target != attempt.accountID {
		return
	}
	change.mu.Lock()
	if change.activeAttempt == attempt {
		change.attemptSucceeded = success
		change.attemptCanceled = attempt.client || errors.Is(attempt.ctx.Err(), context.Canceled)
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
	canceled := attemptCanceled || errors.Is(ctx.Err(), context.Canceled) || (result != nil && result.ClientDisconnect)
	success := succeeded && err == nil && ctx.Err() == nil && result != nil && !result.PartialFailure && !canceled &&
		(result.UpstreamTerminalEvent == "" || result.UpstreamTerminalEvent == "response.completed" || result.UpstreamTerminalEvent == "response.done")
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
	if err != nil || expected.AccountID <= 0 {
		return
	}
	resilience.StateFrom(ctx).SetStickyChange(&openAIStickyChange{
		groupID: derefGroupID(groupID), session: strings.TrimSpace(session), key: key,
		kind: "migrate", expected: expected, ttl: s.openAIStickyBindingTTL(groupID), cache: cache,
	})
}

func planOpenAIStickyReplacement(ctx context.Context, groupID *int64, session, model string, selection *AccountSelectionResult) {
	change := pendingOpenAIStickyChange(ctx)
	if change == nil || change.groupID != derefGroupID(groupID) || change.session != strings.TrimSpace(session) || selection == nil || selection.Account == nil || selection.StickyCapacitySpillover || selection.Account.ID == change.expected.AccountID {
		return
	}
	change.mu.Lock()
	defer change.mu.Unlock()
	if change.target == selection.Account.ID {
		return
	}
	resilience.StateFrom(ctx).SetStickyChange(&openAIStickyChange{
		groupID: change.groupID, session: change.session, key: change.key, model: model,
		kind: "migrate", expected: change.expected, target: selection.Account.ID, ttl: change.ttl, cache: change.cache,
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
	if canceled {
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if !success {
		resilience.StateFrom(ctx).AddAvoided(accountID)
		if _, err := change.cache.RecordSessionBindingFailure(stateCtx, change.groupID, change.key, stickyChangeAvoidSession(change.session), change.expected, accountID, openAIStickyChangeAvoidTTL); err != nil {
			slog.WarnContext(ctx, "sched_quality.takeover_avoid_failed", "account_id", accountID, "error", err)
		}
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
	st := resilience.StateFrom(ctx)
	st.ClearKeptStickyIfOwner(change.expected.AccountID)
	st.NoteStickyBypassBindingChange()
	slog.InfoContext(ctx, "sched_quality.takeover", "component", "sched_quality",
		"kind", change.kind, "gateway_request_id", st.GatewayRequestID(),
		"group_id", change.groupID, "model", change.model,
		"from_account_id", change.expected.AccountID, "to_account_id", change.target)
}
