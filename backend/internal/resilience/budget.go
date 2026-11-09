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
	anchor         time.Time
	bodyReceived   time.Time
	attemptStarted bool
	planSequence   uint64
	base           time.Duration
	budgetStart    string
	attribution    string
	clientFamily   string
	sameAccountMax int
	sameAccount    map[int64]int
	capacitySwitch int
	stateSwitch    int
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
		anchor:         now,
		base:           time.Duration(s.Budget.PreOutputBudgetSeconds) * time.Second,
		budgetStart:    s.Budget.BudgetStart,
		attribution:    s.Budget.BudgetCutAttribution,
		clientFamily:   clientFamilyFromContext(ctx),
		started:        now,
		lastMark:       now,
		maxAttempts:    s.Budget.MaxAttempts,
		sameAccountMax: s.Budget.SameAccountRetryMax,
		sameAccount:    map[int64]int{},
		retryAfterCap:  time.Duration(s.Budget.UpstreamRetryAfterCapSeconds) * time.Second,
		ctx:            ctx,
	}
	if kind == KindText {
		Count("budget_snapshot", 0, 0, "budget_start_mode="+b.budgetStart)
		b.deadline = now.Add(min(b.base, PreOutputWallCap))
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
	deadline := b.admissionDeadlineLocked(false)
	switch {
	case b.ctx != nil && b.ctx.Err() != nil:
		b.exhausted = ExhaustedCanceled
	case b.attempts >= b.maxAttempts:
		b.exhausted = ExhaustedAttempts
	case !deadline.IsZero() && deadline.Sub(now) < minAttemptWindow:
		b.exhausted = ExhaustedDeadline
		if BudgetTraceEnabledForTest {
			b.traceDeadlineForTest("CanAttempt", now, 0, deadline)
		}
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

// admissionDeadlineLocked keeps untimed or not-yet-selected attempts on the
// arrival deadline. A timer witness must come from the guard covering that
// concrete retry, never from a previous attempt's profile or timeout plan.
// Callers hold b.mu; timer plans and observations continue reading b.deadline.
func (b *Budget) admissionDeadlineLocked(hasTimer bool) time.Time {
	if b.kind == KindText && b.budgetStart == BudgetStartBodyEOF && !hasTimer {
		return b.started.Add(min(b.base, PreOutputWallCap))
	}
	return b.deadline
}

// AdmissionRemaining bounds waits before a new attempt. When its timer is not
// yet known, pass false; Remaining remains the timer/observation view of D.
func (b *Budget) AdmissionRemaining(now time.Time, hasTimer bool) time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	deadline := b.admissionDeadlineLocked(hasTimer)
	if deadline.IsZero() {
		return 0
	}
	return max(time.Nanosecond, deadline.Sub(now))
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

// AllowStateSwitch grants the single request-scoped switch reserved for a
// state repair. It is independent from capacity shedding.
func (b *Budget) AllowStateSwitch() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stateSwitch >= 1 {
		return false
	}
	b.stateSwitch++
	return true
}

// ConsumeInPlaceRetry counts a failed attempt that a service retries in
// place, but only when the budget still allows another attempt afterwards
// (and, for sameAccount, the per-account retry cap allows one more);
// otherwise it changes nothing and the failure surfaces to the handler,
// which records it there.
func (b *Budget) ConsumeInPlaceRetry(accountID int64, sameAccount bool, now time.Time, reason ...string) bool {
	return b.ConsumeInPlaceRetryWithTimer(accountID, sameAccount, now, false, reason...)
}

// ConsumeInPlaceRetryWithTimer preserves D only when the retry is covered by
// an existing shared guard. All other callers use the conservative wrapper.
func (b *Budget) ConsumeInPlaceRetryWithTimer(accountID int64, sameAccount bool, now time.Time, hasTimer bool, reason ...string) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	encryptedRepair := len(reason) == 1 && reason[0] == ReasonEncryptedContent
	deadline := b.admissionDeadlineLocked(hasTimer)
	switch {
	case b.ctx != nil && b.ctx.Err() != nil:
		return false
	case !encryptedRepair && b.attempts+1 >= b.maxAttempts:
		return false
	case !deadline.IsZero() && deadline.Sub(now) < minAttemptWindow:
		if BudgetTraceEnabledForTest {
			b.traceDeadlineForTest("ConsumeInPlaceRetry", now, accountID, deadline)
		}
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
	return b.ClampWaitWithTimer(d, now, false)
}

// ClampWaitWithTimer uses the same concrete guard witness as retry admission.
func (b *Budget) ClampWaitWithTimer(d time.Duration, now time.Time, hasTimer bool) time.Duration {
	if b == nil || d <= 0 {
		return d
	}
	raw := d
	if b.retryAfterCap > 0 && d > b.retryAfterCap {
		d = b.retryAfterCap
	}
	if rem := b.AdmissionRemaining(now, hasTimer); rem > 0 && d > rem-minAttemptWindow {
		capped := d
		d = rem - minAttemptWindow
		if d < 0 {
			d = 0
		}
		if BudgetTraceEnabledForTest && d < capped {
			b.traceClampForTest(now, raw, capped, d, rem)
		}
	}
	return d
}

// AttemptTimeout returns the single-attempt first-output timeout: the tier
// timeout, raised to the time the request has already spent, bounded by the
// remaining budget. Raising it makes cut attempts back off geometrically when
// every upstream is slow, while fast failures do not lengthen the next try.
// lastCandidate removes the tier timeout so the last usable account only
// answers to the deadline (design §7.4). Zero means no per-attempt timeout.
func (b *Budget) AttemptTimeout(tier time.Duration, lastCandidate bool, now time.Time) time.Duration {
	return b.PlanAttemptTimeout(tier, lastCandidate, now).Effective()
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
