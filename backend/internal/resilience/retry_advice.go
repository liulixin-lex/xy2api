package resilience

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// RetryAdviceProvider reads the latest local candidate and request snapshots
// at terminal-frame time. Providers must not perform database or Redis I/O,
// retain a pooled HTTP context, or ignore the supplied cancellation signal.
type RetryAdviceProvider func(ctx context.Context) RetryAdvice

const retryAdviceProviderTimeout = 100 * time.Millisecond

var retryAdviceMessageSuffix = regexp.MustCompile(`(^|\.\s+)Please try again in [0-9]+s\.\s*$`)

func (a RetryAdvice) normalized() RetryAdvice {
	if a.Suppress {
		a.Seconds = 0
	} else {
		a.Seconds = max(0, min(a.Seconds, 10))
	}
	return a
}

// Headers returns only the allowlisted Responses error header. Callers
// must check the failed-frame setting before attaching it to a frame.
func (a RetryAdvice) Headers() map[string]string {
	if a.Suppress || a.Seconds <= 0 {
		return nil
	}
	return map[string]string{"retry-after": strconv.Itoa(min(a.Seconds, 10))}
}

// Message replaces only the gateway-generated delay suffix. Unknown or
// suppressed advice removes it; positive advice uses the same cap as Headers.
func (a RetryAdvice) Message(message string) string {
	original := message
	if retryAdviceMessageSuffix.MatchString(message) {
		message = strings.TrimRight(retryAdviceMessageSuffix.ReplaceAllString(message, "$1"), " \t\r\n")
	}
	a = a.normalized()
	if a.Seconds == 0 {
		if strings.TrimSpace(message) == "" {
			return original
		}
		return message
	}
	base := strings.TrimRight(strings.TrimSpace(message), ". ")
	if base != "" {
		base += ". "
	}
	return base + fmt.Sprintf("Please try again in %ds.", a.Seconds)
}

// RetryAdviceEligible separates candidate availability from terminal client or
// business failures. A zero status denotes a synthetic stream error; it needs
// an explicit transient type or code because the wire status is already 200.
func RetryAdviceEligible(status int, errType, code string) bool {
	for _, value := range []string{errType, code} {
		switch strings.ToLower(value) {
		case "invalid_request_error", "invalid_request", "authentication_error", "authentication_failed",
			"permission_error", "permission_denied", "model_not_found", "not_found_error",
			"billing_service_error", "context_length_exceeded", "insufficient_balance", "insufficient_quota",
			"content_policy_violation", "content_filter":
			return false
		}
	}
	if status != 0 {
		return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
	}
	for _, value := range []string{errType, code} {
		switch strings.ToLower(value) {
		case "api_error", "server_error", "upstream_error", "overloaded_error", "server_is_overloaded",
			"rate_limit_error", "rate_limit_exceeded", "no_available_accounts", "account_unavailable":
			return true
		}
	}
	return false
}

// WithRetryAdviceProvider installs lazy advice before selection starts.
// Existing child contexts share the request state, including any explicit
// advice set later by the terminal handler.
func WithRetryAdviceProvider(ctx context.Context, provider RetryAdviceProvider) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	s := StateFrom(ctx)
	if s == nil {
		s = NewRequestState()
		ctx = WithRequestState(ctx, s)
	}
	s.mu.Lock()
	s.retryAdviceProvider = provider
	s.mu.Unlock()
	return ctx
}

// ResolveRetryAdvice prefers explicit handler advice, even explicit unknown,
// over the provider. Lazy results are not cached: a later terminal writer
// must see current candidates and suppression. The caller waits at most
// 100 ms; a canceled or expired result is discarded.
func ResolveRetryAdvice(ctx context.Context) RetryAdvice {
	s := StateFrom(ctx)
	if s == nil {
		return RetryAdvice{}
	}
	s.mu.Lock()
	advice, explicit, provider := s.retryAdvice, s.retryAdviceSet, s.retryAdviceProvider
	s.mu.Unlock()
	if explicit || provider == nil {
		return advice
	}
	queryCtx, cancel := context.WithTimeout(ctx, retryAdviceProviderTimeout)
	defer cancel()
	var result RetryAdvice
	if queryCtx.Err() == nil {
		ready := make(chan RetryAdvice, 1)
		go func() { ready <- provider(queryCtx) }()
		select {
		case result = <-ready:
		case <-queryCtx.Done():
		}
	}
	if queryCtx.Err() != nil {
		result = RetryAdvice{}
	}
	// A terminal cause discovered while querying always takes precedence.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retryAdviceSet {
		return s.retryAdvice
	}
	return result.normalized()
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
	s.retryAdvice = advice.normalized()
	s.retryAdviceSet = true
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
