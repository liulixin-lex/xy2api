package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type openAICollabAffinityKey struct{}
type openAICollabHintKey struct{}
type openAICollabAffinity struct {
	store     *resilience.CollabTreeAffinity
	settings  *resilience.Settings
	groupID   int64
	identity  resilience.CollabTreeHint
	role      resilience.CollabRole
	encrypted bool
	binding   atomic.Pointer[openAICollabThreadBinding]
	disabled  atomic.Bool
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
	return context.WithValue(ctx, openAICollabAffinityKey{}, a)
}

func openAICollabAffinityFrom(ctx context.Context) *openAICollabAffinity {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(openAICollabAffinityKey{}).(*openAICollabAffinity)
	if a != nil && a.disabled.Load() {
		return nil
	}
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

func (s *OpenAIGatewayService) collabThreadAvoidances(ctx context.Context) map[int64]struct{} {
	b := openAICollabBindingFrom(ctx)
	if b == nil {
		return nil
	}
	return b.affinity.store.ThreadAvoidancesAfterReset(ctx, b.avoidScope(), b.affinity.identity.Thread, s.resilience.sessionAccountResetAt)
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
	b := openAICollabBindingFrom(ctx)
	if b == nil || accountID <= 0 {
		return false
	}
	b.avoid(ctx, accountID, avoidUntil)
	_ = b.deleteOwner(ctx, accountID)
	return true
}

func (s *OpenAIGatewayService) AvoidOpenAICollabStateAccount(ctx context.Context, groupID int64, session string, accountID int64, ciphertextFailure bool) {
	if !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	if b := openAICollabBindingFrom(ctx); b != nil && b.matches(groupID, session) && ciphertextFailure {
		avoid := time.Duration(resilience.Current().Session.AvoidSeconds) * time.Second
		if avoid <= 0 {
			avoid = 10 * time.Minute
		}
		NoteOpenAICollabCiphertextFailure(ctx, accountID, time.Now().Add(avoid))
		return
	}
	s.AvoidOpenAIStateAccount(ctx, groupID, session, accountID)
}

// Binding success is owned by today's admission and completion writers.
func NoteOpenAICollabSuccess(ctx context.Context, accountID int64, borrowed bool) {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || borrowed || accountID <= 0 || !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	if a.role == resilience.CollabRoleRoot {
		hint := a.identity
		hint.AccountID = accountID
		a.store.ConfirmTreeHint(ctx, hint)
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
// A present thread binding, including none, suppresses the hint entirely.
func openAICollabHint(ctx context.Context, parentAccountID int64) int64 {
	a := openAICollabAffinityFrom(ctx)
	if a == nil || a.role != resilience.CollabRoleChild || parentAccountID > 0 || resilience.CollabAttemptFrom(ctx).Optional {
		return 0
	}
	if b := openAICollabBindingFrom(ctx); b != nil && b.present() {
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

func withoutCollabSelectionHint(ctx context.Context) context.Context {
	if openAICollabSelectionHint(ctx) == 0 {
		return ctx
	}
	return context.WithValue(ctx, openAICollabHintKey{}, int64(0))
}
