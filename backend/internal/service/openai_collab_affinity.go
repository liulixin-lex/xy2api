package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type openAICollabAffinityKey struct{}
type openAICollabHintKey struct{}
type openAICollabThreadRoutingKey struct{}

type openAICollabThreadRouting struct {
	session string
	avoided map[int64]struct{}
	route   int64
}

type openAICollabAffinity struct {
	store           *resilience.CollabTreeAffinity
	settings        *resilience.Settings
	groupID         int64
	identity        resilience.CollabTreeHint
	role            resilience.CollabRole
	encrypted       bool
	recovering      atomic.Bool
	preserveBinding atomic.Bool
	gateway         atomic.Pointer[OpenAIGatewayService]
}

// WithOpenAICollabTreeAffinity takes the frozen ingress settings and keyed
// identity. Parent uses the thread digest domain, so grandchildren can resolve
// membership. Scope uses the same versioned CollabDigest identity as records;
// the affinity store has its own namespace. Tenant/Group retain ingress isolation.
// No existing session, cache or quality key is changed.
func WithOpenAICollabTreeAffinity(ctx context.Context, store *resilience.CollabTreeAffinity, settings *resilience.Settings, groupID int64, identity resilience.CollabTreeHint, role resilience.CollabRole, encrypted bool) context.Context {
	if ctx == nil || store == nil || !settings.CollabTreeAffinityOn() || identity.Scope.Tenant == "" || identity.Scope.Group == "" || identity.Thread == "" || (role != resilience.CollabRoleRoot && role != resilience.CollabRoleChild) {
		return ctx
	}
	lookup := identity.Scope
	lookup.Tree = ""
	if member, ok := store.LookupThread(ctx, lookup, identity.Thread); ok {
		if member.Parent != identity.Parent || identity.Scope.Tree != "" && identity.Scope.Tree != member.Scope.Tree {
			return ctx
		}
		identity.Scope = member.Scope
	} else if identity.Parent != "" {
		if parent, ok := store.LookupThread(ctx, lookup, identity.Parent); ok {
			if identity.Scope.Tree != "" && identity.Scope.Tree != parent.Scope.Tree {
				return ctx
			}
			identity.Scope = parent.Scope
		}
	}
	if role == resilience.CollabRoleRoot && (identity.Scope.Tree == "" || identity.Parent != "") {
		return ctx
	}
	a := &openAICollabAffinity{store: store, settings: settings, groupID: groupID, identity: identity, role: role, encrypted: encrypted}
	if role == resilience.CollabRoleChild && len(store.ThreadAvoidances(ctx, identity.Scope, identity.Thread)) > 0 {
		a.recovering.Store(true)
		a.preserveBinding.Store(true)
	}
	return context.WithValue(ctx, openAICollabAffinityKey{}, a)
}

func openAICollabAffinityFrom(ctx context.Context) *openAICollabAffinity {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(openAICollabAffinityKey{}).(*openAICollabAffinity)
	if req, ok := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest); a != nil && ok && derefGroupID(req.GroupID) != a.groupID {
		return nil
	}
	return a
}

func openAICollabLeaseSuppressesMigration(ctx context.Context) bool {
	a := openAICollabAffinityFrom(ctx)
	return a != nil && a.settings.CollabLeaseMigrationSuppressionOn() && a.store.HasDeliveryLeases(ctx, a.identity.Scope)
}

func openAICollabQueueReselect(ctx context.Context) bool {
	a := openAICollabAffinityFrom(ctx)
	return a != nil && a.encrypted && a.role == resilience.CollabRoleChild && !resilience.CollabAttemptFrom(ctx).Optional
}

func openAICollabPreserveBinding(ctx context.Context) bool {
	a := openAICollabAffinityFrom(ctx)
	return a != nil && a.role == resilience.CollabRoleChild && a.preserveBinding.Load()
}

func openAICollabThreadAvoidances(ctx context.Context) map[int64]struct{} {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || a.role != resilience.CollabRoleChild {
		return nil
	}
	if routing, ok := ctx.Value(openAICollabThreadRoutingKey{}).(openAICollabThreadRouting); ok {
		return routing.avoided
	}
	return a.store.ThreadAvoidances(ctx, a.identity.Scope, a.identity.Thread)
}

func openAICollabThreadSession(ctx context.Context, a *openAICollabAffinity) string {
	return openAITerminalRejectionSession(ctx, "collab-thread-v1:"+string(a.identity.Scope.Tenant)+":"+string(a.identity.Thread))
}

// Use the existing shared session stores and their normal expiration/reset
// rules. Local hints are only a fallback when a shared dependency is absent.
func (s *OpenAIGatewayService) withOpenAICollabThreadRouting(ctx context.Context, session string) context.Context {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || s == nil {
		return ctx
	}
	a.gateway.CompareAndSwap(nil, s)
	if a.role != resilience.CollabRoleChild {
		return ctx
	}
	routing := openAICollabThreadRouting{session: session, avoided: a.store.ThreadAvoidances(ctx, a.identity.Scope, a.identity.Thread)}
	if s.resilience != nil && s.resilience.store != nil {
		if avoided, err := sessionAvoidAfterReset(ctx, s.resilience.store, a.groupID, openAICollabThreadSession(ctx, a), s.resilience.sessionResetAt); err == nil {
			routing.avoided = avoided
		}
	}
	if route, ok := a.store.LookupThreadRoute(ctx, a.identity.Scope, a.identity.Thread); ok {
		routing.route = route.AccountID
	}
	if s.cache != nil {
		if route, err := s.cache.GetSessionAccountID(ctx, a.groupID, s.openAISessionCacheKey(openAICollabThreadSession(ctx, a))); err == nil || errors.Is(err, ErrStickySessionNotFound) {
			routing.route = route
		}
	}
	if _, avoided := routing.avoided[routing.route]; avoided {
		routing.route = 0
	}
	if len(routing.avoided) > 0 || routing.route > 0 {
		a.recovering.Store(true)
		a.preserveBinding.Store(true)
	}
	return context.WithValue(ctx, openAICollabThreadRoutingKey{}, routing)
}

// NoteOpenAICollabAdmission runs after all admission rechecks, before Forward
// can deliver an executable call. A temporary borrow must not change bindings.
func NoteOpenAICollabAdmission(ctx context.Context, accountID int64, borrowed bool) {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || accountID <= 0 || !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	hint := a.identity
	hint.AccountID = accountID
	if a.role == resilience.CollabRoleRoot && !borrowed {
		a.store.ProvisionalTreeHint(ctx, hint)
	} else {
		a.store.RefreshTreeMembership(ctx, hint)
	}
}

// NoteOpenAICollabCiphertextFailure must run at each existing rejection effect
// boundary, not at final request completion. true replaces tree-wide avoidance
// and unbinding with the existing thread-scoped effect at that same boundary.
// Pass today's avoidance expiry, independently of the tree's sticky TTL.
func NoteOpenAICollabCiphertextFailure(ctx context.Context, accountID int64, avoidUntil time.Time) bool {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || accountID <= 0 || !resilience.CollabThreadPolicy(a.role, resilience.CollabAttemptFrom(ctx), true, false).AvoidNow {
		return false
	}
	a.recovering.Store(true)
	a.preserveBinding.Store(true)
	shared := false
	if s := a.gateway.Load(); s != nil && s.resilience != nil && s.resilience.store != nil {
		if ttl := time.Until(avoidUntil); ttl > 0 {
			// Avoidance only: sticky deletion also follows the request's legacy
			// alias and can accidentally delete the parent's binding.
			shared = s.resilience.store.AddSessionAvoid(ctx, a.groupID, openAICollabThreadSession(ctx, a), accountID, ttl, s.resilience.sessionAccountResetAt(accountID)) == nil
		}
	}
	if !shared {
		a.store.AvoidThread(ctx, resilience.CollabThreadAvoidance{Scope: a.identity.Scope, Thread: a.identity.Thread, AccountID: accountID, ExpiresAt: avoidUntil})
	}
	resilience.StateFrom(ctx).AddAvoided(accountID)
	return true
}

// AvoidOpenAICollabStateAccount replaces an existing avoidance call at the same
// rejection boundary. Other callers of AvoidOpenAIStateAccount keep their policy.
// Classification belongs to the caller; ordinary failures retain today's scope.
func (s *OpenAIGatewayService) AvoidOpenAICollabStateAccount(ctx context.Context, groupID int64, session string, accountID int64, ciphertextFailure bool) {
	if !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	if ciphertextFailure {
		if a := openAICollabAffinityFrom(ctx); a != nil && a.groupID == groupID {
			a.gateway.CompareAndSwap(nil, s)
		}
		avoid := time.Duration(resilience.Current().Session.AvoidSeconds) * time.Second
		if avoid <= 0 {
			avoid = 10 * time.Minute
		}
		if a := openAICollabAffinityFrom(ctx); a != nil && a.groupID == groupID && NoteOpenAICollabCiphertextFailure(ctx, accountID, time.Now().Add(avoid)) {
			return
		}
	}
	s.AvoidOpenAIStateAccount(ctx, groupID, session, accountID)
}

func NoteOpenAICollabSuccess(ctx context.Context, accountID int64, borrowed bool) {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || borrowed || accountID <= 0 || !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	hint := a.identity
	hint.AccountID = accountID
	if a.role == resilience.CollabRoleRoot {
		a.store.ConfirmTreeHint(ctx, hint)
	} else if a.recovering.Load() {
		a.store.BindRecoveredThread(ctx, resilience.CollabThreadRoute{Scope: hint.Scope, Thread: hint.Thread, AccountID: accountID, ExpiresAt: hint.ExpiresAt})
		if s := a.gateway.Load(); s != nil && s.cache != nil {
			// This independent thread binding has no tree aliases and uses
			// exactly the existing sticky TTL.
			_ = s.cache.SetSessionAccountID(ctx, a.groupID, s.openAISessionCacheKey(openAICollabThreadSession(ctx, a)), accountID, s.openAIStickyBindingTTL(&a.groupID))
		}
	}
}

// Only exact client delivery creates a lease. Received/about-to-write events,
// plaintext calls and partial writes cannot suppress a later quality move.
func NoteOpenAICollabLeaseWritten(ctx context.Context, event resilience.CollabDeliveryEvent, lease resilience.CollabDeliveryLease, encrypted bool) bool {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || !encrypted || !event.Executable || event.Phase != resilience.CollabDeliveryPhaseWritten || event.WriteError != nil || event.BytesWritten <= 0 || lease.Key.Scope != a.identity.Scope || event.Generation != lease.Issued.Generation {
		return false
	}
	switch event.EventType {
	case "response.output_item.done", "response.completed", "response.done", "response.json":
	default:
		return false
	}
	return a.store.AcquireDeliveryLease(ctx, lease)
}

// Release each original delivery on its first upstream acceptance. A replaced
// delivery or a delivered local ending uses Terminated, never Consumed.
func ReleaseOpenAICollabLease(ctx context.Context, key resilience.CollabDeliveryKey, reason resilience.CollabLeaseRelease) {
	if a := openAICollabAffinityFrom(ctx); a != nil && key.Scope == a.identity.Scope {
		a.store.ReleaseDeliveryLease(ctx, key, reason)
	}
}

// Existing parent binding wins; the model-free tree is only its fallback.
// A recovered child's own route precedes both. No optional attempt binds.
func openAICollabHint(ctx context.Context, parentAccountID int64) int64 {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || a.role != resilience.CollabRoleChild || resilience.CollabAttemptFrom(ctx).Optional {
		return 0
	}
	if routing, loaded := ctx.Value(openAICollabThreadRoutingKey{}).(openAICollabThreadRouting); loaded {
		if routing.route > 0 {
			return routing.route
		}
	} else if route, ok := a.store.LookupThreadRoute(ctx, a.identity.Scope, a.identity.Thread); ok && !a.store.ThreadAvoided(ctx, resilience.CollabThreadAvoidance{Scope: a.identity.Scope, Thread: a.identity.Thread, AccountID: route.AccountID}) {
		return route.AccountID
	}
	if parentAccountID > 0 {
		return 0
	}
	if hint, ok := a.store.LookupTreeHint(ctx, a.identity.Scope); ok {
		return hint.AccountID
	}
	return 0
}

// Preferences apply only within the same quality/trial and pinning round.
// Normal slot acquisition, trial reservation, rechecks and retries still own
// admission. Wait-plan ordering deliberately does not call this helper.
func openAICollabSelectionLess(a, b selectionKey, hint int64) bool {
	if hint > 0 && a.ID != b.ID && a.Tier == b.Tier && a.Pinned == b.Pinned && (a.ID == hint || b.ID == hint) {
		return a.ID == hint
	}
	return selectionLess(a, b)
}

func openAICollabSelectionHint(ctx context.Context) int64 {
	id, _ := ctx.Value(openAICollabHintKey{}).(int64)
	return id
}

func openAICollabHintAdmitted(ctx context.Context, accountID int64) {
	if accountID == openAICollabSelectionHint(ctx) {
		if a := openAICollabAffinityFrom(ctx); a != nil {
			a.preserveBinding.Store(true)
		}
	}
}
