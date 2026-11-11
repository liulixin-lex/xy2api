package service

import (
	"context"
	"time"
)

// OpenAIStickyRebindCache replaces a binding only while its owner is unchanged.
// Both modern and legacy session hashes use the same atomic operation.
type OpenAIStickyRebindCache interface {
	CompareAndSwapSessionAccountID(context.Context, int64, string, int64, int64, time.Duration) (bool, error)
}

// gatewaySessionBindingReader reads a binding and its remaining lifetime in
// one round trip. Every hit refreshes the binding to its full TTL, so the
// session's idle time is TTL minus the remaining lifetime.
type gatewaySessionBindingReader interface {
	GetSessionAccountIDWithTTL(ctx context.Context, groupID int64, sessionHash string) (int64, time.Duration, error)
}
