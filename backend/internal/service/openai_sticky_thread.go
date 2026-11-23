package service

import (
	"context"
	"time"
)

// StickyThreadBinding describes a read-only parent and a writable thread key.
// Observe is diagnostic/fallback state only; storage never consults it.
type StickyThreadBinding struct {
	Key, Parent, Legacy string
	Fallback            bool
	TTL, RefreshTTL     time.Duration
	Observe             func(int64, time.Duration, bool, bool)
	Disable             func()
}

type stickyThreadBindingKey struct{}

func WithStickyThreadBinding(ctx context.Context, binding StickyThreadBinding) context.Context {
	return context.WithValue(ctx, stickyThreadBindingKey{}, binding)
}

func StickyThreadBindingFrom(ctx context.Context) (StickyThreadBinding, bool) {
	b, ok := ctx.Value(stickyThreadBindingKey{}).(StickyThreadBinding)
	return b, ok
}
