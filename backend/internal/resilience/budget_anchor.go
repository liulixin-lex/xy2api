package resilience

import (
	"context"
	"strings"
	"time"
)

const (
	BudgetStartArrival       = "arrival"
	BudgetStartBodyEOF       = "body_eof"
	BudgetAttributionOff     = "off"
	BudgetAttributionObserve = "observe"
	BudgetAttributionEnforce = "enforce"
	// PreOutputWallCap is a policy constant for every text path, independent of edge.
	PreOutputWallCap    = 290 * time.Second
	BodyAnchorThreshold = time.Second
)

type clientFamilyKey struct{}

// WithClientFamily snapshots only a bounded family label, never a raw User-Agent.
func WithClientFamily(ctx context.Context, ua string) context.Context {
	family := "other"
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "codex"):
		family = "codex"
	case strings.Contains(ua, "claude"):
		family = "claude"
	case strings.Contains(ua, "cursor"):
		family = "cursor"
	case strings.Contains(ua, "cline"):
		family = "cline"
	case strings.Contains(ua, "openai"):
		family = "openai_sdk"
	case strings.Contains(ua, "anthropic"):
		family = "anthropic_sdk"
	}
	return context.WithValue(ctx, clientFamilyKey{}, family)
}
func clientFamilyFromContext(ctx context.Context) string {
	if ctx != nil {
		if v, ok := ctx.Value(clientFamilyKey{}).(string); ok {
			return v
		}
	}
	return "other"
}

// DeadlineAnchor affects only the absolute deadline; Started and lastMark stay at arrival.
func (b *Budget) DeadlineAnchor() time.Time {
	if b == nil {
		return time.Time{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.anchor
}

// MarkBodyReceived accepts only the first successful receipt. Starting a timer
// or installing an attempt profile freezes the anchor, even before Attempts increments.
func (b *Budget) MarkBodyReceived(at time.Time, basis string) {
	if b == nil || at.IsZero() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.bodyReceived.IsZero() {
		return
	}
	b.bodyReceived = at
	if b.kind != KindText || b.attemptStarted || b.budgetStart != BudgetStartBodyEOF || at.Sub(b.started) < BodyAnchorThreshold {
		return
	}
	b.anchor = at
	b.deadline = at.Add(b.base)
	if cap := b.started.Add(PreOutputWallCap); cap.Before(b.deadline) {
		b.deadline = cap
	}
}

// MarkAttemptStarted freezes the deadline without changing attempt accounting.
func (b *Budget) MarkAttemptStarted() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.attemptStarted = true
	b.mu.Unlock()
}
