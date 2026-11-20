package service

import (
	"context"
	"time"
)

// StickySessionBinding identifies one incarnation of a session's owner.
// Generation is opaque and must never be reused after deletion or expiry.
type StickySessionBinding struct {
	AccountID  int64
	Generation string
	// LegacyKey is an alias of the canonical key, never a second CAS authority.
	LegacyKey string
}

// Alias operations share the primary owner's generation and never let an
// alias outlive it. Resolution adopts a legacy-only owner without changing it.
type OpenAIStickyAliasCache interface {
	ResolveSessionBinding(context.Context, int64, string, string, bool) (StickySessionBinding, error)
	SyncSessionBindingAlias(context.Context, int64, string, string, int64, time.Duration, bool) error
	DeleteSessionBindingAliases(context.Context, int64, string, string, int64) (bool, error)
}

// FailureScope is cleared on success. RejectScope, when set, fences a target
// against live avoidances written at or after its most recent account reset.
type BindingCommitOptions struct {
	FailureScope  string
	RejectScope   string
	TargetResetAt time.Time
}

type BindingCommitStatus string

const (
	BindingCommitted     BindingCommitStatus = "committed"
	BindingChanged       BindingCommitStatus = "binding_changed"
	BindingTargetAvoided BindingCommitStatus = "target_avoided"
	BindingCommitError   BindingCommitStatus = "error"
)

type BindingCommitResult struct {
	Status      BindingCommitStatus
	ObservedGen string
	NewGen      string
}

type OpenAIStickyVersionedCache interface {
	GetSessionBinding(context.Context, int64, string) (StickySessionBinding, error)
	// A successful commit also clears the target from the supplied failure-avoidance scope.
	CommitSessionBinding(context.Context, int64, string, StickySessionBinding, int64, time.Duration, BindingCommitOptions) (BindingCommitResult, error)
	// Returns the stored expiry in Unix milliseconds, or zero if the binding changed.
	RecordSessionBindingFailure(context.Context, int64, string, string, StickySessionBinding, int64, time.Duration, ...time.Time) (int64, error)
}

// gatewaySessionBindingReader reads a binding and its remaining lifetime in
// one round trip. Every hit refreshes the binding to its full TTL, so the
// session's idle time is TTL minus the remaining lifetime.
type gatewaySessionBindingReader interface {
	GetSessionAccountIDWithTTL(ctx context.Context, groupID int64, sessionHash string) (int64, time.Duration, error)
}
