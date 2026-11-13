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

type OpenAIStickyVersionedCache interface {
	GetSessionBinding(context.Context, int64, string) (StickySessionBinding, error)
	CommitSessionBinding(context.Context, int64, string, StickySessionBinding, int64, time.Duration) (bool, error)
	RecordSessionBindingFailure(context.Context, int64, string, string, StickySessionBinding, int64, time.Duration, ...time.Time) (bool, error)
}

// gatewaySessionBindingReader reads a binding and its remaining lifetime in
// one round trip. Every hit refreshes the binding to its full TTL, so the
// session's idle time is TTL minus the remaining lifetime.
type gatewaySessionBindingReader interface {
	GetSessionAccountIDWithTTL(ctx context.Context, groupID int64, sessionHash string) (int64, time.Duration, error)
}
