package resilience

import (
	"context"
	"strconv"
)

// RetryAdvice is a request's client retry hint, not an account's cooldown.
// One second denotes a healthy alternative; known recovery is clamped to
// 2–10 seconds by the advisor. Zero means that recovery is unknown.
// Suppress is terminal and takes precedence over every earlier hint.
// Source is an internal explanation and must not contain upstream text.
type RetryAdvice struct {
	Seconds  int
	Source   string
	Suppress bool
}

// Headers returns only the allowlisted Responses error header. Callers
// must check the failed-frame setting before attaching it to a frame.
func (a RetryAdvice) Headers() map[string]string {
	if a.Suppress || a.Seconds <= 0 {
		return nil
	}
	return map[string]string{"retry-after": strconv.Itoa(min(a.Seconds, 10))}
}

// WithRetryAdvice attaches a request state if needed and updates its advice.
// Existing child contexts share that state: a writer created before the
// final failure sees a later turn cap instead of a stale retry delay.
// Attach request state before starting workers; never share it across requests.
func WithRetryAdvice(ctx context.Context, advice RetryAdvice) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if StateFrom(ctx) == nil {
		ctx = WithRequestState(ctx, NewRequestState())
	}
	SetRetryAdvice(ctx, advice)
	return ctx
}

// SetRetryAdvice updates an already attached request state. It returns false
// without state; an entry point can use WithRetryAdvice to attach one.
// A terminal suppression cannot be undone later in the same request.
func SetRetryAdvice(ctx context.Context, advice RetryAdvice) bool {
	s := StateFrom(ctx)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retryAdvice.Suppress {
		return true
	}
	if advice.Suppress {
		advice.Seconds = 0
	} else {
		advice.Seconds = max(0, min(advice.Seconds, 10))
	}
	s.retryAdvice = advice
	return true
}

// RetryAdviceFrom returns a snapshot, or empty advice without request state.
func RetryAdviceFrom(ctx context.Context) RetryAdvice {
	s := StateFrom(ctx)
	if s == nil {
		return RetryAdvice{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retryAdvice
}
