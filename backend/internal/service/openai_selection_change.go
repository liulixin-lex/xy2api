package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	reason, errorClass, expiry := pendingOpenAIStickyChange(ctx).writeFirstOutputAvoid(ctx, fact)
	fields := []any{"component", "sched_quality", "gateway_request_id", resilience.StateFrom(ctx).GatewayRequestID(),
		"group_id", fact.GroupID, "model", fact.Model, "account_id", fact.AccountID, "owner_gen", logKnown(fact.Generation), "expires_at_ms", logKnown(expiry), "reason", reason, "confirmed", reason == "confirmed"}
	if errorClass != "" {
		fields = append(fields, "error_class", errorClass)
	}
	slog.InfoContext(ctx, "sched_quality.first_output_avoid", fields...)
}

func (c *openAIStickyChange) writeFirstOutputAvoid(ctx context.Context, fact resilience.OwnerFirstOutputCutoff) (reason, errorClass string, expiry int64) {
	cfg := resilience.Current()
	switch {
	case !cfg.SessionOn():
		return "session_disabled", "", 0
	case !fact.CanTransfer:
		return "protocol_ineligible", "", 0
	case !c.matchesCutoff(fact):
		return "binding_changed", "", 0
	case c.cache == nil:
		return "error", "cache_unavailable", 0
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	var reset time.Time
	if c.resetAt != nil {
		reset = c.resetAt(fact.AccountID)
	}
	ttl := time.Until(fact.FiredAt.Add(time.Duration(cfg.Session.AvoidSeconds) * time.Second))
	if ttl <= 0 {
		return "expired", "", 0
	}
	written, err := c.cache.RecordSessionBindingFailure(stateCtx, fact.GroupID, fact.BindingKey, firstOutputAvoidSession(fact.Session), c.expected, fact.AccountID, ttl, reset)
	if ctxErr := stateCtx.Err(); ctxErr != nil {
		err = ctxErr // A late acknowledgement cannot authorize handoff.
	}
	if err != nil {
		if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
			return "timeout", fmt.Sprintf("%T", netErr), 0
		}
		return "error", fmt.Sprintf("%T", err), 0
	}
	if written == 0 {
		return "rejected", "", 0
	}
	if !resilience.StateFrom(ctx).ConfirmOwnerAvoid(fact.AttemptSeq, fact.Generation) {
		return "receipt_stale", "", written
	}
	return "confirmed", "", written
}

// A selection only plans a change. Admission and billing must never commit it.
type openAIStickyChange struct {
	mu               sync.Mutex
	done             bool
	followDelivered  bool
	attemptSucceeded bool
	attemptCanceled  bool
	activeAttempt    *schedAttemptCapture

	recheckFollow                 func(context.Context) string
	ownerFirstOutputLimit         time.Duration // Frozen on the original send, only for first_output_follow.
	canTransfer                   bool          // Final selection's protocol authorization for expected.
	targetUpstreamFirstValidDelay time.Duration
	targetUpstreamFirstValidKnown bool

	fromPriority, toPriority *int
	voluntary                *voluntarySwitchEvidence // Non-nil opts this plan into the atomic first-output target check.
	groupID                  int64
	session                  string
	key                      string
	model                    string
	kind                     string
	reason                   string
	fromModel, toModel       string
	ownerLookup              func(context.Context, int64) *Account
	trialSession             string
	leases                   SchedTrialLeases
	trial                    *selectionTrialReservation
	expected                 StickySessionBinding
	target                   int64
	ttl                      time.Duration
	cache                    OpenAIStickyVersionedCache
	dualWriteLegacy          bool
	skipCommit               bool // The snapshot read failed; keep all selection writes suppressed.
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
	var failover *UpstreamFailoverError
	beforeSend := change.kind == stickyFollow && errors.As(err, &failover) && failover.PreDispatchSelectionInvalidated
	change.mu.Lock()
	canceled := change.attemptCanceled || resilience.StateFrom(ctx).ClientCanceled() || (result != nil && result.ClientDisconnect)
	success := change.attemptSucceeded && err == nil && ctx.Err() == nil && !canceled && result.CompletedForDelivery()
	change.completeLocked(ctx, success, canceled, beforeSend)
}

const stickyAbandonIdentityConflict = "identity_conflict"

// AbandonOpenAIStickyForwardForIdentityConflict retires only this candidate's
// sticky plan after a Responses conversion identity conflict. The caller must
// invoke it only for that fixed conversion reason; the old binding is preserved
// and the request can prepare a fresh plan for its next candidate.
func AbandonOpenAIStickyForwardForIdentityConflict(ctx context.Context, accountID int64) {
	state := resilience.StateFrom(ctx)
	change := pendingOpenAIStickyChange(ctx)
	if state == nil || change == nil || change.target != accountID {
		return
	}
	change.mu.Lock()
	if change.done {
		change.mu.Unlock()
		return
	}
	change.reason = stickyAbandonIdentityConflict
	change.completeLocked(ctx, false, false, false)
	state.SetStickyChange(nil)
}

func pendingOpenAIStickyChange(ctx context.Context) *openAIStickyChange {
	change, _ := resilience.StateFrom(ctx).StickyChange().(*openAIStickyChange)
	return change
}

func preservePendingOpenAIStickyBinding(ctx context.Context, groupID *int64, session string) bool {
	return pendingOpenAIStickyChange(ctx).matchesSession(groupID, session)
}

func (c *openAIStickyChange) matchesSession(groupID *int64, session string) bool {
	return c != nil && c.groupID == derefGroupID(groupID) && c.session == strings.TrimSpace(session)
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
	legacy := s.openAILegacySessionCacheKey(ctx, session)
	fallback := s.openAISessionHashReadOldFallbackEnabled()
	if aliases, ok := s.cache.(OpenAIStickyAliasCache); ok {
		if legacy != "" && (fallback || s.openAISessionHashDualWriteOldEnabled()) {
			expected, err = aliases.ResolveSessionBinding(ctx, derefGroupID(groupID), key, legacy, fallback)
		}
	} else if (err == nil && expected.AccountID <= 0 || errors.Is(err, ErrStickySessionNotFound)) && fallback && legacy != "" {
		key = legacy
		expected, err = cache.GetSessionBinding(ctx, derefGroupID(groupID), key)
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
	if selection != nil && selection.Account != nil && change.expected.AccountID == selection.Account.ID {
		change.fromPriority = new(selection.Account.Priority)
	}
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
	next := change.replacement(kind, model, selection.Account.ID, resilience.StateFrom(ctx).StickyBypassReason())
	next.toPriority = new(selection.Account.Priority)
	next.toModel = OpenAIAttemptModel(selection.Account, model)
	resilience.StateFrom(ctx).SetStickyChange(next)
}

func (change *openAIStickyChange) replacement(kind, model string, target int64, reason string) *openAIStickyChange {
	return &openAIStickyChange{
		groupID: change.groupID, session: change.session, key: change.key, model: model,
		fromPriority: change.fromPriority,
		kind:         kind, expected: change.expected, target: target, ttl: change.ttl, cache: change.cache,
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
		if change.followDelivered {
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
		reason = "existing_change" // Another binding plan owns this outcome.
	case selection.StickyCapacitySpillover || openAIStickyBindingKept(ctx, session, target) || keptOwner != 0 && keptOwner != target:
		reason = "borrowed"
	default:
		view, now := s.selectionView(), s.selectionNow()
		if selectionState(view, fact.GroupID, selection.Account, model).Placement(selectionAuto(view, fact.GroupID, now), now) != schedquality.PlaceFront {
			reason = "not_front"
		}
	}
	if reason != "" {
		declined := &openAIStickyChange{groupID: fact.GroupID, expected: StickySessionBinding{AccountID: fact.AccountID, Generation: fact.Generation},
			kind: stickyFollow, target: target, model: model, toPriority: new(selection.Account.Priority), toModel: OpenAIAttemptModel(selection.Account, model), fromModel: fact.Model}
		if change != nil && change.expected.AccountID == fact.AccountID {
			change.mu.Lock()
			declined.fromPriority = change.fromPriority
			if declined.fromModel == "" {
				declined.fromModel = change.fromModel
			}
			change.mu.Unlock()
		}
		declined.logSwitch(ctx, reason, BindingCommitResult{})
		return
	}
	change.mu.Lock()
	next := change.replacement(stickyFollow, model, target, "faster_delivery")
	change.mu.Unlock()
	next.toPriority = new(selection.Account.Priority)
	next.toModel = OpenAIAttemptModel(selection.Account, model)
	if fact.Model != "" {
		next.fromModel = fact.Model
	}
	next.ownerFirstOutputLimit = fact.Effective
	req, _ := ContextWithSelectionProfitGate(ctx, selection).Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	req.GroupID, req.SessionHash, req.RequestedModel, req.Platform = group, session, model, PlatformOpenAI
	req.ExcludedIDs = nil // Request exclusions governed selection; the target already delivered.
	account := selection.Account
	next.recheckFollow = func(checkCtx context.Context) string {
		checkCtx = ContextWithSelectionProfitGate(CopyOpenAIQualityRoutingContext(checkCtx, ctx), selection)
		return s.followTargetRejection(checkCtx, req, account)
	}
	state.SetStickyChange(next)
}

// Only a fast, complete delivery reaches this gate. Reuse the scheduler's
// current account and hard-admission checks without acquiring another slot.
func (s *OpenAIGatewayService) followTargetRejection(ctx context.Context, req OpenAIAccountScheduleRequest, account *Account) string {
	groupID := derefGroupID(req.GroupID)
	req.RequirePrivacySet = s.loadOpenAIGroupRequiresPrivacySet(ctx, req.GroupID)
	ctx = context.WithValue(s.withOpenAIQuotaAutoPauseContext(ctx), openAIGroupPrivacyRequirementContextKey{},
		openAIGroupPrivacyRequirement{groupID: groupID, required: req.RequirePrivacySet})
	fresh := s.recheckSelectionAccount(ctx, req, account)
	if fresh == nil || fresh.ID != account.ID || !s.openAIAccountMatchesSchedulingGroup(fresh, req.GroupID) {
		return "target_ineligible"
	}
	view, now := s.selectionView(), s.selectionNow()
	if selectionState(view, groupID, fresh, req.RequestedModel).Placement(selectionAuto(view, groupID, now), now) != schedquality.PlaceFront {
		return "not_front"
	}
	if s.qualityOwnsSticky(ctx, req.GroupID, req.SessionHash) {
		return "quality_owned"
	}
	// The original quality snapshot may predate an update by another instance.
	q := qualityRequest(ctx)
	if remote, ok := s.cache.(OpenAIQualityRoutingStore); ok && q != nil && q.group == groupID && q.session == req.SessionHash && s.qualityConfig().EffectiveMode() == "enforce" {
		q.mu.Lock()
		scope := q.scope
		q.mu.Unlock()
		current, _, err := remote.ReadOpenAIQualityRouting(ctx, scope, groupID, nil)
		if err != nil {
			return "quality_unavailable"
		}
		if current != nil && current.Generation > 0 && current.Expires > time.Now().UnixMilli() {
			return "quality_owned"
		}
	}
	return ""
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
	change.completeLocked(ctx, success, canceled, false)
}

// completeLocked owns the caller's lock. Legacy changes coordinate the slot's
// trial lease through completion. A follow target is front-row and has no
// reservation, so its one claimed outcome can perform storage I/O independently.
func (change *openAIStickyChange) completeLocked(ctx context.Context, success, canceled, beforeSend bool) {
	if change.done {
		change.mu.Unlock()
		return
	}
	change.done = true
	if change.kind != stickyFollow {
		defer change.mu.Unlock()
		change.finish(ctx, success, canceled, beforeSend)
		return
	}
	change.followDelivered = success
	final := change.replacement(change.kind, change.model, change.target, change.reason)
	final.toPriority, final.toModel = change.toPriority, change.toModel
	final.ownerFirstOutputLimit, final.recheckFollow = change.ownerFirstOutputLimit, change.recheckFollow
	final.activeAttempt, final.attemptSucceeded, final.attemptCanceled = change.activeAttempt, change.attemptSucceeded, change.attemptCanceled
	final.targetUpstreamFirstValidKnown, final.targetUpstreamFirstValidDelay = change.targetUpstreamFirstValidKnown, change.targetUpstreamFirstValidDelay
	change.mu.Unlock()
	final.finish(ctx, success, canceled, beforeSend)
}

// The trace belongs to this capture, including when a repair retries the same
// account. Read it after releasing change.mu (capture completion takes it last).
func (a *schedAttemptCapture) followAttemptSent() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dispatch.AttemptSeq != 0
}

// Zero values of these fields designate unavailable evidence. Priorities and
// idle settings use pointers because zero is a real policy value.
func logKnown[T comparable](value T) *T {
	var zero T
	if value == zero {
		return nil
	}
	return &value
}

func (change *openAIStickyChange) logSwitch(ctx context.Context, reason string, result BindingCommitResult, extra ...any) {
	fields := []any{"component", "sched_quality", "gateway_request_id", resilience.StateFrom(ctx).GatewayRequestID(),
		"group_id", change.groupID, "model", change.model, "from_model", logKnown(change.fromModel), "to_model", logKnown(change.toModel), "from", change.expected.AccountID, "to", logKnown(change.target),
		"kind", change.kind, "reason", reason, "committed", result.Status == BindingCommitted,
		"expected_gen", logKnown(change.expected.Generation), "observed_gen", logKnown(result.ObservedGen), "new_gen", logKnown(result.NewGen),
		"commit_attempted", result.Status != "", "commit_result", logKnown(result.Status), "attempt_seq", change.activeAttempt.sentSequence(),
		"from_priority", change.fromPriority, "to_priority", change.toPriority}
	if v := change.voluntary; v != nil && change.kind == "takeover" {
		fields = append(fields, "takeover_idle_setting", v.idleSetting, "takeover_idle_observed", v.idleObserved,
			"avoid_seen_at_select", v.avoidSeen, "session_on", v.sessionOn, "transfer_allowed", v.transferAllowed, "quality_owned", v.qualityOwned)
	}
	slog.InfoContext(ctx, "sched_quality.session_switch", append(fields, extra...)...)
}

func (change *openAIStickyChange) finish(ctx context.Context, success, canceled, beforeSend bool) {
	var result BindingCommitResult
	fasterDelivery := false
	defer func() {
		if result.Status == BindingCommitted && change.trial != nil {
			change.trial.promote(ctx)
		}
		if result.Status != BindingCommitted {
			change.releaseTrial(ctx)
		}
		reason := change.reason
		if reason == "" {
			reason = change.kind
		}
		var extra []any
		if change.kind == stickyFollow {
			extra = append(extra, "faster_complete", fasterDelivery)
		}
		change.logSwitch(ctx, reason, result, extra...)
	}()
	if canceled && change.kind == stickyFollow {
		change.reason = "canceled"
	}
	if change.reason == stickyAbandonIdentityConflict {
		return
	}
	if canceled || change.skipCommit {
		return
	}
	if change.kind == stickyFollow && (beforeSend || !change.activeAttempt.followAttemptSent()) {
		change.reason = "pre_dispatch_invalidated"
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
		resilience.StateFrom(ctx).AddAvoided(change.target)
		var reset time.Time
		if change.resetAt != nil {
			reset = change.resetAt(change.target)
		}
		if _, err := change.cache.RecordSessionBindingFailure(stateCtx, change.groupID, change.key, stickyChangeAvoidSession(change.session), change.expected, change.target, openAIStickyChangeAvoidTTL, reset); err != nil {
			slog.WarnContext(ctx, "sched_quality.takeover_avoid_failed", "account_id", change.target, "error_class", fmt.Sprintf("%T", err))
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
	if change.kind == stickyFollow {
		if reason := change.recheckFollow(stateCtx); reason != "" {
			change.reason = reason
			return
		}
		if ctx.Err() != nil || resilience.StateFrom(ctx).ClientCanceled() {
			change.reason = "canceled"
			return
		}
		if !resilience.Current().SessionOn() {
			change.reason = "session_disabled"
			return
		}
	}
	options := BindingCommitOptions{FailureScope: stickyChangeAvoidSession(change.session)}
	if change.voluntary != nil {
		options.RejectScope = firstOutputAvoidSession(change.session)
		if change.resetAt != nil {
			options.TargetResetAt = change.resetAt(change.target)
		}
	}
	var err error
	result, err = change.cache.CommitSessionBinding(stateCtx, change.groupID, change.key, change.expected, change.target, change.ttl, options)
	if err != nil {
		result = BindingCommitResult{Status: BindingCommitError}
		if change.kind == stickyFollow {
			change.reason = "commit_error"
		}
		slog.WarnContext(ctx, "sched_quality.takeover_commit_failed", "account_id", change.target, "error_class", fmt.Sprintf("%T", err))
		return
	}
	if result.Status != BindingCommitted {
		if change.kind == stickyFollow || result.Status == BindingTargetAvoided {
			change.reason = string(result.Status)
		}
		return
	}
	if change.leases != nil && change.expected.AccountID > 0 && change.expected.AccountID != change.target {
		model := change.fromModel
		if model == "" && change.model != "" && change.ownerLookup != nil {
			// A slow successful commit can consume most of its timeout before cleanup starts.
			lookupCtx, cancelLookup := resilienceStateContext(ctx)
			owner := change.ownerLookup(lookupCtx, change.expected.AccountID)
			cancelLookup()
			if owner != nil && owner.ID == change.expected.AccountID {
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
			if err := aliases.SyncSessionBindingAlias(stateCtx, change.groupID, change.key, change.expected.LegacyKey, change.target, min(change.ttl, 10*time.Minute), true); err != nil {
				slog.WarnContext(ctx, "sched_quality.binding_alias_failed", "account_id", change.target, "error_class", fmt.Sprintf("%T", err))
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
