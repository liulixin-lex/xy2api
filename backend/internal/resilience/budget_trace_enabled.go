//go:build unit || gt0512

package resilience

import (
	"context"
	"sync"
	"time"
)

const BudgetTraceEnabledForTest = true

// BudgetTraceForTest is an opt-in, per-request recorder. Install it before
// NewBudget; it never invokes user code while a budget/timer lock is held.
// It has no connection to TimingJSON, metrics, usage, or ops persistence.
type BudgetTraceForTest struct {
	mu      sync.Mutex
	profile AttemptProfile
	events  []BudgetEventForTest
}

// Durations are exact nanoseconds, not rounded production diagnostics.
type TimeoutPlanForTest struct {
	Sequence         uint64         `json:"sequence"`
	Profile          AttemptProfile `json:"profile"`
	Source           string         `json:"source"`
	Tier             time.Duration  `json:"tier_ns"`
	Planned          time.Duration  `json:"planned_ns"`
	Effective        time.Duration  `json:"effective_ns"`
	Remaining        time.Duration  `json:"remaining_ns"`
	StartOffset      time.Duration  `json:"start_offset_ns"`
	CommittedAtStart bool           `json:"committed_at_start"`
	Would            bool           `json:"would_budget_cut"`
	Applied          bool           `json:"budget_cut_applied"`
}

type BudgetEventForTest struct {
	Sequence        uint64             `json:"sequence"`
	Kind            string             `json:"kind"`
	Site            string             `json:"site"`
	Stage           string             `json:"stage"`
	At              time.Time          `json:"at"`
	Offset          time.Duration      `json:"offset_ns"`
	DecisionAt      time.Time          `json:"decision_at"`
	DecisionOffset  time.Duration      `json:"decision_offset_ns"`
	AccountID       int64              `json:"account_id"`
	Profile         AttemptProfile     `json:"profile"`
	HasPlan         bool               `json:"has_plan"`
	Plan            TimeoutPlanForTest `json:"plan"`
	DeadlineLimited bool               `json:"deadline_limited"`
	RawInput        time.Duration      `json:"raw_input_ns"`
	Input           time.Duration      `json:"input_ns"`
	Output          time.Duration      `json:"output_ns"`
	Remaining       time.Duration      `json:"remaining_ns"`
	Attempts        int                `json:"attempts"`
}

type budgetTraceKey struct{}

func WithBudgetTraceForTest(ctx context.Context) (context.Context, *BudgetTraceForTest) {
	trace := &BudgetTraceForTest{}
	return context.WithValue(ctx, budgetTraceKey{}, trace), trace
}

func budgetTraceFrom(ctx context.Context) *BudgetTraceForTest {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(budgetTraceKey{}).(*BudgetTraceForTest)
	return trace
}

func (t *BudgetTraceForTest) Events() []BudgetEventForTest {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]BudgetEventForTest(nil), t.events...)
}

func (t *BudgetTraceForTest) append(e BudgetEventForTest) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e.Sequence = uint64(len(t.events) + 1)
	if !e.HasPlan {
		e.Profile = t.profile
		if e.AccountID == 0 {
			e.AccountID = e.Profile.AccountID
		}
	}
	t.events = append(t.events, e)
}

type timeoutPlanTrace struct {
	trace   *BudgetTraceForTest
	state   *RequestState
	origin  time.Time
	profile AttemptProfile
	site    string
}

func captureTimeoutPlanForTest(b *Budget, p *TimeoutPlan, profile AttemptProfile, state *RequestState) {
	if b == nil {
		return
	}
	trace := budgetTraceFrom(b.ctx)
	if trace == nil {
		return
	}
	p.testTrace = timeoutPlanTrace{trace: trace, state: state, origin: b.started, profile: profile}
	p.traceEventForTest("plan_created", "plan", time.Now())
}

func (p TimeoutPlan) traceEventForTest(kind, site string, at time.Time) {
	t := p.testTrace
	if t.trace == nil {
		return
	}
	if t.site != "" {
		site = t.site
	}
	stage := ""
	if t.state != nil {
		t.state.d.mu.Lock()
		stage = t.state.d.stage
		t.state.d.mu.Unlock()
	}
	t.trace.append(BudgetEventForTest{
		Kind: kind, Site: site, Stage: stage, At: at, Offset: at.Sub(t.origin),
		AccountID: t.profile.AccountID, Profile: t.profile, HasPlan: true,
		Plan: TimeoutPlanForTest{Sequence: p.seq, Profile: t.profile, Source: p.source,
			Tier: p.tier, Planned: p.planned, Effective: p.effective, Remaining: p.remaining,
			StartOffset: p.startOffset, CommittedAtStart: p.committed, Would: p.would, Applied: p.applied},
		DeadlineLimited: kind == "timer_fired" && p.remaining > 0 && p.effective == p.remaining,
		Input:           p.planned, Output: p.effective, Remaining: p.remaining,
	})
}

// ObserveFiredForTest must be called only by the timer's firing branch, before
// cancellation/closing/draining can move its timestamp. Never call it from an
// error formatter or reconstruct the plan from the current AttemptProfile.
func (p TimeoutPlan) ObserveFiredForTest(site string) {
	p.traceEventForTest("timer_fired", site, time.Now())
}

func (p TimeoutPlan) WithTimerSiteForTest(site string) TimeoutPlan {
	p.testTrace.site = site
	return p
}

func (b *Budget) traceProfileForTest(profile AttemptProfile) {
	if b == nil {
		return
	}
	if t := budgetTraceFrom(b.ctx); t != nil {
		t.mu.Lock()
		t.profile = profile
		t.mu.Unlock()
		now := time.Now()
		t.append(BudgetEventForTest{Kind: "profile_set", Site: "SetAttemptProfile", At: now, Offset: now.Sub(b.started)})
	}
}

// Called under b.mu at the actual rejecting branch. The recorder only takes
// its own lock; it cannot re-enter the budget or alter its result.
func (b *Budget) traceDeadlineForTest(site string, at time.Time, accountID int64) {
	if t := budgetTraceFrom(b.ctx); t != nil {
		now := time.Now()
		t.append(BudgetEventForTest{Kind: "deadline_rejected", Site: site, At: now,
			Offset: now.Sub(b.started), DecisionAt: at, DecisionOffset: at.Sub(b.started), AccountID: accountID, DeadlineLimited: true,
			Input: minAttemptWindow, Remaining: b.deadline.Sub(at), Attempts: b.attempts})
	}
}

func (b *Budget) traceClampForTest(at time.Time, raw, capped, output, remaining time.Duration) {
	if t := budgetTraceFrom(b.ctx); t != nil {
		now := time.Now()
		t.append(BudgetEventForTest{Kind: "wait_clamped", Site: "ClampWait", At: now,
			Offset: now.Sub(b.started), DecisionAt: at, DecisionOffset: at.Sub(b.started), DeadlineLimited: true,
			RawInput: raw, Input: capped, Output: output, Remaining: remaining})
	}
}

// The caller has already established that its Remaining-limited wait expired
// with an error while the parent context stayed live.
func ObserveSlotWaitForTest(ctx context.Context, slotType string, id int64, input, output time.Duration) {
	b := StateFrom(ctx).Budget()
	if b == nil {
		return
	}
	if t := budgetTraceFrom(b.ctx); t != nil {
		now := time.Now()
		accountID := int64(0)
		if slotType == "account" {
			accountID = id
		}
		t.append(BudgetEventForTest{Kind: "slot_wait_exhausted", Site: slotType, At: now,
			Offset: now.Sub(b.started), AccountID: accountID, DeadlineLimited: true,
			RawInput: input, Input: input, Output: output, Remaining: output})
	}
}

// BudgetBaseSecondsForTest is intentionally available only in test builds.
// The value was copied from NewBudget's base and survives FinishTiming.
func (s *RequestState) BudgetBaseSecondsForTest() (int64, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetBaseSeconds, s.budgetBaseSet
}
