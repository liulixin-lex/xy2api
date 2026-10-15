package resilience

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// RequestKind selects the budget shape of a request.
type RequestKind int

// Request kinds.
const (
	// KindText is a text request (Responses, Chat, Messages, Gemini text).
	KindText RequestKind = iota
	// KindImage is an image or asynchronous image request: no pre-output
	// deadline and no first-output timeout (design §6.1, §7.4).
	KindImage
)

// Exhaustion reasons reported by Budget.
const (
	ExhaustedAttempts = "attempts"
	ExhaustedDeadline = "deadline"
	ExhaustedCanceled = "canceled"
)

// minAttemptWindow is the least remaining pre-output time that still allows
// another attempt; a shorter attempt could not produce output in time.
const minAttemptWindow = 2 * time.Second

// Budget is the per-request attempt and time budget (design §6). It is safe
// for concurrent use; a nil *Budget means budgets are disabled and every
// method allows the 0.3.0 behaviour.
type Budget struct {
	mu             sync.Mutex
	kind           RequestKind
	started        time.Time
	maxAttempts    int
	attempts       int
	deadline       time.Time
	sameAccountMax int
	sameAccount    map[int64]int
	capacitySwitch int
	outputStarted  bool
	exhausted      string
	retryAfterCap  time.Duration
	ctx            context.Context
}

// NewBudget creates the budget of one request, or nil when budgets are off.
func NewBudget(ctx context.Context, s *Settings, kind RequestKind, now time.Time) *Budget {
	if !s.BudgetOn() {
		return nil
	}
	b := &Budget{
		kind:           kind,
		started:        now,
		maxAttempts:    s.Budget.MaxAttempts,
		sameAccountMax: s.Budget.SameAccountRetryMax,
		sameAccount:    map[int64]int{},
		retryAfterCap:  time.Duration(s.Budget.UpstreamRetryAfterCapSeconds) * time.Second,
		ctx:            ctx,
	}
	if kind == KindText {
		b.deadline = now.Add(time.Duration(s.Budget.PreOutputBudgetSeconds) * time.Second)
	}
	return b
}

type budgetKey struct{}

// WithBudget attaches b to ctx.
func WithBudget(ctx context.Context, b *Budget) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, budgetKey{}, b)
}

// BudgetFrom returns the budget attached to ctx, or nil.
func BudgetFrom(ctx context.Context) *Budget {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(budgetKey{}).(*Budget)
	return b
}

// Kind returns the request kind.
func (b *Budget) Kind() RequestKind {
	if b == nil {
		return KindText
	}
	return b.kind
}

// Canceled reports whether the client or forwarding context is gone.
func (b *Budget) Canceled() bool {
	return b != nil && b.ctx != nil && b.ctx.Err() != nil
}

// MarkOutputStarted records that semantic output reached the client; the
// pre-output deadline no longer applies.
func (b *Budget) MarkOutputStarted() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.outputStarted = true
	b.mu.Unlock()
}

// OutputStarted reports whether semantic output reached the client.
func (b *Budget) OutputStarted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.outputStarted
}

// RecordAttempt counts one finished upstream attempt.
func (b *Budget) RecordAttempt() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.attempts++
	b.mu.Unlock()
}

// Attempts returns the number of finished attempts.
func (b *Budget) Attempts() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempts
}

// CanAttempt reports whether another upstream attempt may start. When it
// returns false, Exhausted reports why.
func (b *Budget) CanAttempt(now time.Time) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.ctx != nil && b.ctx.Err() != nil:
		b.exhausted = ExhaustedCanceled
	case b.attempts >= b.maxAttempts:
		b.exhausted = ExhaustedAttempts
	case !b.outputStarted && !b.deadline.IsZero() && b.deadline.Sub(now) < minAttemptWindow:
		b.exhausted = ExhaustedDeadline
	default:
		return true
	}
	return false
}

// Exhausted returns the reason of the last failed CanAttempt, or "".
func (b *Budget) Exhausted() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exhausted
}

// Remaining returns the remaining pre-output time, or zero when the request
// has no pre-output deadline.
func (b *Budget) Remaining(now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deadline.IsZero() || b.outputStarted {
		return 0
	}
	if d := b.deadline.Sub(now); d > 0 {
		return d
	}
	return time.Nanosecond
}

// HasDeadline reports whether the request has a pre-output deadline.
func (b *Budget) HasDeadline() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.deadline.IsZero()
}

// Deadline returns the pre-output deadline (zero when none).
func (b *Budget) Deadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deadline
}

// AllowSameAccountRetry consumes one same-account retry for the account when
// the per-account cap allows it (design §6.2).
func (b *Budget) AllowSameAccountRetry(accountID int64) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sameAccount[accountID] >= b.sameAccountMax {
		return false
	}
	b.sameAccount[accountID]++
	return true
}

// AllowCapacitySwitch consumes the single account switch allowed after
// request-scoped capacity shedding (design §6.3).
func (b *Budget) AllowCapacitySwitch() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.capacitySwitch >= 1 {
		return false
	}
	b.capacitySwitch++
	return true
}

// ClampWait bounds an upstream-requested wait by the configured cap and the
// remaining pre-output time.
func (b *Budget) ClampWait(d time.Duration, now time.Time) time.Duration {
	if b == nil || d <= 0 {
		return d
	}
	if b.retryAfterCap > 0 && d > b.retryAfterCap {
		d = b.retryAfterCap
	}
	if rem := b.Remaining(now); rem > 0 && d > rem-minAttemptWindow {
		d = rem - minAttemptWindow
		if d < 0 {
			d = 0
		}
	}
	return d
}

// AttemptTimeout returns the single-attempt first-output timeout: the tier
// timeout bounded by the remaining budget. lastCandidate removes the tier
// timeout so the last usable account only answers to the deadline (design
// §7.4). Zero means no per-attempt timeout.
func (b *Budget) AttemptTimeout(tier time.Duration, lastCandidate bool, now time.Time) time.Duration {
	if b == nil {
		return tier
	}
	rem := b.Remaining(now)
	if lastCandidate {
		return rem
	}
	if rem > 0 && (tier <= 0 || rem < tier) {
		return rem
	}
	return tier
}

// FirstOutputTier returns the first-output timeout tier for an OpenAI request
// by model override, then reasoning effort (design §7.4).
func FirstOutputTier(s *Settings, model, effort string) time.Duration {
	if s == nil {
		return 0
	}
	t := s.Budget.FirstOutputTimeoutSeconds
	if v, ok := modelOverride(s.Budget.ModelOverrides, model); ok {
		return time.Duration(v) * time.Second
	}
	secs := t.Default
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high":
		secs = t.High
	case "xhigh":
		secs = t.XHigh
	case "max":
		secs = t.Max
	}
	return time.Duration(secs) * time.Second
}

// modelOverride matches an exact model or a "prefix*" entry; the longest
// prefix wins.
func modelOverride(overrides map[string]int, model string) (int, bool) {
	if len(overrides) == 0 {
		return 0, false
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return 0, false
	}
	best, bestLen, found := 0, -1, false
	for k, v := range overrides {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == model {
			return v, true
		}
		if strings.HasSuffix(k, "*") {
			prefix := strings.TrimSuffix(k, "*")
			if strings.HasPrefix(model, prefix) && len(prefix) > bestLen {
				best, bestLen, found = v, len(prefix), true
			}
		}
	}
	return best, found
}

// Jitter returns d scaled by a uniform factor in [1-ratio, 1+ratio].
func Jitter(d time.Duration, ratio float64) time.Duration {
	if d <= 0 || ratio <= 0 {
		return d
	}
	f := 1 - ratio + rand.Float64()*2*ratio
	return time.Duration(float64(d) * f)
}

// Sleep waits for d or until ctx is done; it reports whether the full wait
// completed.
func Sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx == nil || ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	if ctx == nil {
		<-t.C
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
