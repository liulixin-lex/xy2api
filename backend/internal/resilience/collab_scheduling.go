package resilience

import (
	"context"
	"time"
)

// Preferences only reorder an already admitted candidate set, stably within
// its existing quality/admission round. They never add/remove candidates or
// wait. Missing/unusable hints leave the original order exactly unchanged.
type CollabAccountPreference struct {
	AccountID int64
	Kind      CollabHintKind
}

type CollabBindingPolicy struct {
	Borrow                      bool
	PreserveAdmissionBinding    bool
	PreserveSuccessBinding      bool
	UntilFirstCollaborationCall bool
}

type CollabSchedulingFlags struct {
	TreeHintImmediateOnly    bool
	SuppressQualityMigration bool
	AllowQueueReselect       bool
}

type CollabTreeHint struct {
	Scope     CollabRecordScope
	Thread    CollabDigest
	Parent    CollabDigest
	AccountID int64
	Status    CollabTreeHintStatus
	ExpiresAt time.Time
}

type CollabDeliveryLease struct {
	Key       CollabDeliveryKey
	Issued    CollabIssuedToken
	AccountID int64
	ExpiresAt time.Time
}

type CollabThreadAvoidance struct {
	Scope     CollabRecordScope
	Thread    CollabDigest
	AccountID int64
	ExpiresAt time.Time
}

type CollabSchedulingRequest struct {
	Scope       CollabRecordScope
	Thread      CollabDigest
	Parent      CollabDigest
	Preferences []CollabAccountPreference
	Binding     CollabBindingPolicy
	Flags       CollabSchedulingFlags
}

// Tree hints are independent, model-free tenant/group keys. Admission writes a
// provisional root hint before any executable output; successful roots confirm.
// Child admissions refresh membership/TTL without overwriting the root hint.
// Lookup does not replace model support or profitability admission checks.
// Borrow failures return original bytes to the original account, preserving
// bindings at both admission and success. Ciphertext state avoidance is thread
// scoped at the existing rejection timing, never postponed to final failure.
// Successful recovery binds only that thread. Optional attempts do neither.
// Tree hints are usable only with an immediate slot; otherwise select normally.
type CollabScheduler interface {
	BindRecoveredThread(context.Context, CollabThreadRoute) bool
	LookupThreadRoute(context.Context, CollabRecordScope, CollabDigest) (CollabThreadRoute, bool)
	OrderAccounts(context.Context, CollabSchedulingRequest, []int64) []int64
	BindingPolicy(context.Context, CollabSchedulingRequest) CollabBindingPolicy
	ProvisionalTreeHint(context.Context, CollabTreeHint) bool
	ConfirmTreeHint(context.Context, CollabTreeHint) bool
	LookupTreeHint(context.Context, CollabRecordScope) (CollabTreeHint, bool)
	RefreshTreeMembership(context.Context, CollabTreeHint) bool
	LookupThread(context.Context, CollabRecordScope, CollabDigest) (CollabTreeHint, bool)
	AcquireDeliveryLease(context.Context, CollabDeliveryLease) bool
	ReleaseDeliveryLease(context.Context, CollabDeliveryKey, CollabLeaseRelease)
	HasDeliveryLeases(context.Context, CollabRecordScope) bool
	AvoidThread(context.Context, CollabThreadAvoidance) bool
	ThreadAvoided(context.Context, CollabThreadAvoidance) bool
}

type NoopCollabScheduler struct{}

func (NoopCollabScheduler) OrderAccounts(_ context.Context, _ CollabSchedulingRequest, candidates []int64) []int64 {
	return candidates
}
func (NoopCollabScheduler) BindingPolicy(context.Context, CollabSchedulingRequest) CollabBindingPolicy {
	return CollabBindingPolicy{}
}
func (NoopCollabScheduler) ProvisionalTreeHint(context.Context, CollabTreeHint) bool { return false }
func (NoopCollabScheduler) ConfirmTreeHint(context.Context, CollabTreeHint) bool     { return false }
func (NoopCollabScheduler) LookupTreeHint(context.Context, CollabRecordScope) (CollabTreeHint, bool) {
	return CollabTreeHint{}, false
}
func (NoopCollabScheduler) RefreshTreeMembership(context.Context, CollabTreeHint) bool { return false }
func (NoopCollabScheduler) LookupThread(context.Context, CollabRecordScope, CollabDigest) (CollabTreeHint, bool) {
	return CollabTreeHint{}, false
}
func (NoopCollabScheduler) AcquireDeliveryLease(context.Context, CollabDeliveryLease) bool {
	return false
}
func (NoopCollabScheduler) ReleaseDeliveryLease(context.Context, CollabDeliveryKey, CollabLeaseRelease) {
}
func (NoopCollabScheduler) HasDeliveryLeases(context.Context, CollabRecordScope) bool { return false }
func (NoopCollabScheduler) AvoidThread(context.Context, CollabThreadAvoidance) bool   { return false }
func (NoopCollabScheduler) ThreadAvoided(context.Context, CollabThreadAvoidance) bool { return false }

type CollabThreadRoute struct {
	Scope     CollabRecordScope
	Thread    CollabDigest
	AccountID int64
	ExpiresAt time.Time
}

func (NoopCollabScheduler) BindRecoveredThread(context.Context, CollabThreadRoute) bool { return false }
func (NoopCollabScheduler) LookupThreadRoute(context.Context, CollabRecordScope, CollabDigest) (CollabThreadRoute, bool) {
	return CollabThreadRoute{}, false
}

// Even a confirmed tree hint cannot introduce a queue wait.
func CollabTreeHintUsable(hint CollabTreeHint, immediateSlot bool) bool {
	return hint.AccountID > 0 && immediateSlot
}

// These actions replace only tree-wide handling for a child's ciphertext state
// failure. Avoidance uses the current rejection point, not the final endpoint.
// Recovery follows the successful account for this thread without tree rebinding.
type CollabThreadRoutingPolicy struct {
	ThreadScoped  bool
	AvoidNow      bool
	BindRecovered bool
}

func CollabThreadPolicy(role CollabRole, attempt CollabAttempt, ciphertextFailure, recovered bool) CollabThreadRoutingPolicy {
	if role != CollabRoleChild || !ciphertextFailure || !attempt.AccountEffectsAllowed() {
		return CollabThreadRoutingPolicy{}
	}
	return CollabThreadRoutingPolicy{ThreadScoped: true, AvoidNow: !recovered, BindRecovered: recovered}
}
