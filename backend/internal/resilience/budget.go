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
	lastMark       time.Time
	lastAttempt    time.Duration
	deadline       time.Time
	sameAccountMax int
	sameAccount    map[int64]int
	capacitySwitch int
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
		lastMark:       now,
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

// Started returns when the request started.
func (b *Budget) Started() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.started
}

// Kind returns the request kind.
func (b *Budget) Kind() RequestKind {
	if b == nil {
		return KindText
	}
	return b.kind
}

// RecordAttemptAt counts one finished upstream attempt and returns its
// approximate duration: the time since the request started or since the
// previous attempt ended (selection time is included and small).
func (b *Budget) RecordAttemptAt(now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attempts++
	d := now.Sub(b.lastMark)
	b.lastMark = now
	if d < 0 {
		d = 0
	}
	b.lastAttempt = d
	return d
}

// LastAttemptDuration returns the duration of the latest recorded attempt.
func (b *Budget) LastAttemptDuration() time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastAttempt
}

// RetryAfterCap returns the configured cap of upstream-requested waits.
func (b *Budget) RetryAfterCap() time.Duration {
	if b == nil {
		return 0
	}
	return b.retryAfterCap
}

// MaxAttempts returns the attempt limit.
func (b *Budget) MaxAttempts() int {
	if b == nil {
		return 0
	}
	return b.maxAttempts
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
	case !b.deadline.IsZero() && b.deadline.Sub(now) < minAttemptWindow:
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
	if b.deadline.IsZero() {
		return 0
	}
	if d := b.deadline.Sub(now); d > 0 {
		return d
	}
	return time.Nanosecond
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

// ConsumeInPlaceRetry counts a failed attempt that a service retries in
// place, but only when the budget still allows another attempt afterwards
// (and, for sameAccount, the per-account retry cap allows one more);
// otherwise it changes nothing and the failure surfaces to the handler,
// which records it there.
func (b *Budget) ConsumeInPlaceRetry(accountID int64, sameAccount bool, now time.Time) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.ctx != nil && b.ctx.Err() != nil:
		return false
	case b.attempts+1 >= b.maxAttempts:
		return false
	case !b.deadline.IsZero() && b.deadline.Sub(now) < minAttemptWindow:
		return false
	case sameAccount && b.sameAccount[accountID] >= b.sameAccountMax:
		return false
	}
	b.attempts++
	if d := now.Sub(b.lastMark); d > 0 {
		b.lastAttempt = d
	}
	b.lastMark = now
	if sameAccount {
		b.sameAccount[accountID]++
	}
	return true
}

// CapacitySwitchUsed reports whether the capacity-shedding switch was used.
func (b *Budget) CapacitySwitchUsed() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.capacitySwitch > 0
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
