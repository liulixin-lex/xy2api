package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
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

const stickyFollow = "first_output_follow"

func firstOutputAvoidSession(session string) string {
	return "first_output:" + strings.TrimSpace(session)
}

func firstOutputMigrationAllowed(ctx context.Context, session string) bool {
	if scope, ok := resilience.StateFrom(ctx).GatewayRequestScope(); ok && scope != resilience.GatewayRequestScopeText {
		return false
	}
	req, _ := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	if req.RequiredCapability == OpenAIEndpointCapabilitySeedance {
		return false
	}
	protected, _ := ctx.Value(openAITransferProtectedKey{}).(bool)
	_, guardian := openAIGuardianParentAffinityFromContext(ctx)
	return !protected && session != "" && !openAINoStickyMigration(ctx) && !guardian &&
		!openAIRejectionScope(ctx).preserveBinding && !preserveOpenAIRejectionBinding(ctx, session)
}

func (s *OpenAIGatewayService) firstOutputQualityOwnsSticky(ctx context.Context, group *int64, session string) bool {
	if q := qualityRequest(ctx); q != nil && q.group == derefGroupID(group) && q.session == session {
		s.loadQuality(ctx, q)
	}
	return s.qualityOwnsSticky(ctx, group, session)
}

func (c *openAIStickyChange) matchesCutoff(f resilience.OwnerFirstOutputCutoff) bool {
	return c != nil && c.groupID == f.GroupID && c.session == f.Session && c.key == f.BindingKey &&
		c.expected.AccountID == f.AccountID && c.expected.Generation == f.Generation && !c.skipCommit
}

func recordFirstOutputAvoid(ctx context.Context, fact resilience.OwnerFirstOutputCutoff) {
	state := resilience.StateFrom(ctx)
	confirmed := false
	reason := "protocol_ineligible"
	if !resilience.Current().SessionOn() {
		reason = "session_disabled"
	} else if fact.CanTransfer {
		if change := pendingOpenAIStickyChange(ctx); change.matchesCutoff(fact) && change.writeFirstOutputAvoid(ctx, fact) {
			confirmed = state.ConfirmOwnerAvoid(fact.AttemptSeq, fact.Generation)
		}
		reason = "unconfirmed"
		if confirmed {
			reason = "confirmed"
		}
	}
	slog.InfoContext(ctx, "sched_quality.first_output_avoid", "component", "sched_quality", "gateway_request_id", state.GatewayRequestID(),
		"group_id", fact.GroupID, "model", fact.Model, "account_id", fact.AccountID, "reason", reason, "confirmed", confirmed)
}

func (c *openAIStickyChange) writeFirstOutputAvoid(ctx context.Context, fact resilience.OwnerFirstOutputCutoff) bool {
	if c.cache == nil {
		return false
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	var reset time.Time
	if c.resetAt != nil {
		reset = c.resetAt(fact.AccountID)
	}
	ttl := time.Until(fact.FiredAt.Add(time.Duration(resilience.Current().Session.AvoidSeconds) * time.Second))
	if ttl <= 0 {
		return false
	}
	written, err := c.cache.RecordSessionBindingFailure(stateCtx, fact.GroupID, fact.BindingKey, firstOutputAvoidSession(fact.Session), c.expected, fact.AccountID, ttl, reset)
	return written && err == nil && stateCtx.Err() == nil
}

// A selection only plans a change. Admission and billing must never commit it.
type openAIStickyChange struct {
	mu               sync.Mutex
	done             bool
	commitAttempted  bool
	attemptSucceeded bool
	attemptCanceled  bool
	activeAttempt    *schedAttemptCapture

	ownerFirstOutputLimit         time.Duration // Frozen on the original send, only for first_output_follow.
	canTransfer                   bool          // Final selection's protocol authorization for expected.
	targetUpstreamFirstValidDelay time.Duration
	targetUpstreamFirstValidKnown bool

	groupID         int64
	session         string
	key             string
	model           string
	kind            string
	reason          string
	fromModel       string
	ownerLookup     func(context.Context, int64) *Account
	trialSession    string
	leases          SchedTrialLeases
	trial           *selectionTrialReservation
	expected        StickySessionBinding
	target          int64
	ttl             time.Duration
	cache           OpenAIStickyVersionedCache
	dualWriteLegacy bool
	skipCommit      bool // The snapshot read failed; keep all selection writes suppressed.
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
		if !attempt.upstreamFirstValidAt.IsZero() {
			change.targetUpstreamFirstValidDelay = attempt.upstreamFirstValidAt.Sub(attempt.started)
			change.targetUpstreamFirstValidKnown = true
		}
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
		leases:          s.selectionTrialLeases(), trialSession: session, ownerLookup: s.selectionTrialOwner,
	})
}

type openAITransferProtectedKey struct{}

// WithOpenAITransferProtected carries an ingress protocol constraint solely
// for first-output avoidance and transfer. Other selection rules stay in force.
func WithOpenAITransferProtected(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAITransferProtectedKey{}, true)
}

// Runs after planOpenAIStickyReplacement (deferred later by the scheduler).
// Quality ownership and child-thread protection are only final after selection.
func (s *OpenAIGatewayService) freezeOpenAITransferEligibility(ctx context.Context, groupID *int64, session string, selection *AccountSelectionResult, ordinary bool) {
	change := pendingOpenAIStickyChange(ctx)
	if change == nil {
		return
	}
	eligible := ordinary && selection != nil && selection.Account != nil && !selection.StickyCapacitySpillover &&
		change.expected.AccountID == selection.Account.ID && change.expected.Generation != "" && !change.skipCommit &&
		change.groupID == derefGroupID(groupID) && session != "" && change.session == session && change.key != "" &&
		firstOutputMigrationAllowed(ctx, session) && !s.qualityOwnsSticky(ctx, groupID, session)
	change.mu.Lock()
	change.canTransfer = eligible
	change.mu.Unlock()
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
	kind := "owner_released"
	if change.expected.AccountID == 0 {
		kind = "create"
	}
	resilience.StateFrom(ctx).SetStickyChange(change.replacement(kind, model, selection.Account.ID, resilience.StateFrom(ctx).StickyBypassReason()))
}

func (change *openAIStickyChange) replacement(kind, model string, target int64, reason string) *openAIStickyChange {
	return &openAIStickyChange{
		groupID: change.groupID, session: change.session, key: change.key, model: model,
		kind: kind, expected: change.expected, target: target, ttl: change.ttl, cache: change.cache,
		dualWriteLegacy: change.dualWriteLegacy, skipCommit: change.skipCommit, ownerReleased: change.ownerReleased,
		resetAt: change.resetAt, leases: change.leases, fromModel: change.fromModel,
		ownerLookup: change.ownerLookup, trialSession: change.trialSession, reason: reason,
	}
}

// Only the final scheduler result includes trial borrowing and last-resort state.
// Each reselection retires the previous follow target, even if admission later
// sends the same account under a protected protocol.
func (s *OpenAIGatewayService) planOpenAIFollow(ctx context.Context, group *int64, session, model string, selection *AccountSelectionResult, ordinary bool) {
	state := resilience.StateFrom(ctx)
	fact, hit := state.OwnerCutoff()
	if !hit {
		return
	}
	change := pendingOpenAIStickyChange(ctx)
	if change != nil && change.kind == stickyFollow {
		change.mu.Lock()
		if change.commitAttempted {
			change.mu.Unlock()
			return
		}
		change.done = true
		base := change.replacement("migrate", change.model, 0, "")
		change.mu.Unlock()
		state.SetStickyChange(base)
		change = base
	}
	if selection == nil || selection.Account == nil || fact.AccountID == selection.Account.ID {
		return
	}
	target := selection.Account.ID
	_, confirmed := state.ConfirmedOwnerCutoff()
	keptOwner := state.KeptSticky()
	reason := ""
	switch {
	case !resilience.Current().SessionOn():
		reason = "session_disabled"
	case !ordinary || !fact.CanTransfer || !firstOutputMigrationAllowed(ctx, session) || s.qualityOwnsSticky(ctx, group, session):
		reason = "protocol_ineligible"
	case !confirmed:
		reason = "avoid_unconfirmed"
	case !change.matchesCutoff(fact) || fact.GroupID != derefGroupID(group) || fact.Session != session:
		reason = "binding_changed"
	case change.ownerReleased || change.target != 0:
		return // An independently planned create, release or takeover owns this outcome.
	case selection.StickyCapacitySpillover || openAIStickyBindingKept(ctx, session, target) || keptOwner != 0 && keptOwner != target:
		reason = "borrowed"
	default:
		view, now := s.selectionView(), s.selectionNow()
		if selectionState(view, fact.GroupID, selection.Account, model).Placement(selectionAuto(view, fact.GroupID, now), now) != schedquality.PlaceFront {
			reason = "not_front"
		}
	}
	if reason != "" {
		slog.InfoContext(ctx, "sched_quality.session_switch", "component", "sched_quality", "gateway_request_id", state.GatewayRequestID(),
			"group_id", fact.GroupID, "model", model, "from", fact.AccountID, "to", target, "kind", stickyFollow, "reason", reason, "committed", false)
		return
	}
	next := change.replacement(stickyFollow, model, target, "faster_delivery")
	next.ownerFirstOutputLimit = fact.Effective
	state.SetStickyChange(next)
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
	committed, fasterDelivery := false, false
	defer func() {
		if committed && change.trial != nil {
			change.trial.promote(ctx)
		}
		if !committed {
			change.releaseTrial(ctx)
		}
		reason := change.reason
		if reason == "" {
			reason = change.kind
		}
		fields := []any{"component", "sched_quality",
			"gateway_request_id", resilience.StateFrom(ctx).GatewayRequestID(), "group_id", change.groupID,
			"model", change.model, "from", change.expected.AccountID, "to", change.target,
			"kind", change.kind, "reason", reason, "committed", committed}
		if change.kind == stickyFollow {
			fields = append(fields, "faster_complete", fasterDelivery, "commit_attempted", change.commitAttempted)
		}
		slog.InfoContext(ctx, "sched_quality.session_switch", fields...)
	}()
	if canceled && change.kind == stickyFollow {
		change.reason = "canceled"
	}
	if canceled || change.skipCommit {
		return
	}
	if change.kind == stickyFollow && !resilience.Current().SessionOn() {
		change.reason = "session_disabled"
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if !success {
		if change.kind == stickyFollow {
			change.reason = "incomplete_delivery"
		}
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
	if change.kind == stickyFollow {
		switch {
		case !change.attemptSucceeded || change.attemptCanceled:
			change.reason = "incomplete_delivery"
			return
		case !change.targetUpstreamFirstValidKnown || change.targetUpstreamFirstValidDelay < 0 || change.ownerFirstOutputLimit <= 0:
			change.reason = "missing_clock"
			return
		case change.targetUpstreamFirstValidDelay >= change.ownerFirstOutputLimit:
			change.reason = "not_faster"
			return
		}
		fasterDelivery = true
	}
	if openAIStickyBindingKept(ctx, change.session, change.target) {
		if change.kind == stickyFollow {
			change.reason = "borrowed"
		}
		return
	}
	change.commitAttempted = true
	changed, err := change.cache.CommitSessionBinding(stateCtx, change.groupID, change.key, change.expected, change.target, change.ttl, stickyChangeAvoidSession(change.session))
	if err != nil {
		if change.kind == stickyFollow {
			change.reason = "commit_error"
		}
		slog.WarnContext(ctx, "sched_quality.takeover_commit_failed", "account_id", accountID, "error", err)
		return
	}
	if !changed {
		if change.kind == stickyFollow {
			change.reason = "binding_changed"
		}
		return
	}
	committed = true
	if change.leases != nil && change.expected.AccountID > 0 && change.expected.AccountID != change.target {
		model := change.fromModel
		if model == "" && change.ownerLookup != nil {
			if owner := change.ownerLookup(stateCtx, change.expected.AccountID); owner != nil {
				model = OpenAIAttemptModel(owner, change.model)
			}
		}
		// Without the owner's mapping, let the member expire instead of removing
		// a member under the client model, which may name a different upstream key.
		if model != "" {
			releaseSelectionTrial(ctx, change.leases, change.expected.AccountID, model, change.groupID, TrialLeaseSessionMember(change.trialSessionKey()))
		}
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
}

func (c *openAIStickyChange) trialSessionKey() string {
	if c.trialSession != "" {
		return c.trialSession
	}
	return c.session
}

// A failed candidate owns only its request member, never a concurrent
// request's reservation or a committed session member.
func (c *openAIStickyChange) releaseTrial(ctx context.Context) { c.trial.release(ctx) }
