package service

import (
	"context"
	"sync"
	"time"
)

type nativeDeferredFlushKey struct{}
type nativeDeferredFlushState struct {
	mu     sync.Mutex
	readAt time.Time
}

// WithNativeStreamDeferredFlush marks a worker writer whose Flush only enqueues
// a journal event. Real network delivery records its own measured completion.
func WithNativeStreamDeferredFlush(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeDeferredFlushKey{}, &nativeDeferredFlushState{})
}
func NativeStreamFlushDeferred(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	state, _ := ctx.Value(nativeDeferredFlushKey{}).(*nativeDeferredFlushState)
	return state != nil
}

// RecordNativeStreamEventRead is called with the complete upstream frame read
// time BEFORE classification/Write. The existing relay parser owns that time.
func RecordNativeStreamEventRead(ctx context.Context, readAt time.Time) {
	if ctx == nil {
		return
	}
	state, _ := ctx.Value(nativeDeferredFlushKey{}).(*nativeDeferredFlushState)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.readAt = readAt
	state.mu.Unlock()
}
func ConsumeNativeStreamEventReadTime(ctx context.Context) time.Time {
	if ctx == nil {
		return time.Time{}
	}
	state, _ := ctx.Value(nativeDeferredFlushKey{}).(*nativeDeferredFlushState)
	if state == nil {
		return time.Time{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	value := state.readAt
	state.readAt = time.Time{}
	return value
}
func RecordNativeAttachmentFlush(ctx context.Context, readAt, flushedAt time.Time) {
	if ctx == nil || readAt.IsZero() {
		return
	}
	// Preserve scheduling identity/policy, removing only the intermediate-writer
	// flag. Replayed history deliberately does not count as gateway live latency.
	ctx = context.WithValue(ctx, nativeDeferredFlushKey{}, (*nativeDeferredFlushState)(nil))
	RecordNativeStreamFlush(ctx, readAt, flushedAt)
}
