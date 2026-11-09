package resilience

import (
	"encoding/json"
	"time"
)

// RequestTiming contains observations only. In particular CDNObserved comes
// from forgeable client headers and must never drive budget or scheduling.
// Pointers distinguish an observed zero from an unavailable/historical value.
type RequestTiming struct {
	BodyBytes               *int64  `json:"body_bytes,omitempty"`
	FirstReadMs             *int64  `json:"first_read_ms,omitempty"`
	BodyEOFMs               *int64  `json:"body_eof_ms,omitempty"`
	AnchorBasis             *string `json:"anchor_basis,omitempty"`
	AnchorEligible          *bool   `json:"anchor_eligible,omitempty"`
	ReceiptSource           *string `json:"receipt_source,omitempty"`
	BodyReceiptRepeat       *int64  `json:"body_receipt_repeat,omitempty"`
	PreDispatchMs           *int64  `json:"pre_dispatch_ms,omitempty"`
	KeepaliveCommitMs       *int64  `json:"keepalive_commit_ms,omitempty"`
	WireStatus              *int64  `json:"wire_status,omitempty"`
	DispatchCount           *int64  `json:"dispatch_count,omitempty"`
	MinDispatchRemainingMs  *int64  `json:"min_dispatch_remaining_ms,omitempty"`
	FinalDeadlineMs         *int64  `json:"final_deadline_ms,omitempty"`
	LateAttemptNoTimerCount *int64  `json:"-"`
	BudgetStartMode         *string `json:"budget_start_mode,omitempty"`
	BudgetT0UnixMs          *int64  `json:"budget_t0_unix_ms,omitempty"`
	LateAttemptCount        *int64  `json:"-"`
	ClientFamily            *string `json:"client_family,omitempty"`
	ClientCancelMs          *int64  `json:"client_cancel_ms,omitempty"`
	CancelStage             *string `json:"cancel_stage,omitempty"`
	CDNObserved             *string `json:"cdn_observed,omitempty"`
}

type ResponseStageTiming struct {
	StartOffsetMs int64 `json:"start_offset_ms"`
	WindowMs      int64 `json:"window_ms"`
}

type timingTrace struct {
	firstDispatch   time.Time
	keepaliveCommit time.Time
	wireStatus      int64
	dispatchCount   int64
	minRemaining    time.Duration
	attemptTimers   map[time.Time]bool // timer coverage known before dispatch
	attemptMetrics  map[time.Time]bool // dispatched late owner -> frozen timer coverage
	responseStage   *ResponseStageTiming
}

func observed[T any](v T) *T { return &v }

// Timing copies state without invoking delivery/keepalive probes (lock order).
func (s *RequestState) Timing() RequestTiming {
	if s == nil {
		return RequestTiming{}
	}
	s.mu.Lock()
	b, firstRead, eof := s.budget, s.bodyFirstRead, s.bodyReceived
	bytes, repeat, edge, trace := s.bodyBytes, s.bodyReceiptRepeat, s.edge, s.timing
	receiptSource := s.bodyReceiptBasis
	lateCount := len(s.timing.attemptMetrics)
	lateNoTimer := int64(0)
	for _, timed := range s.timing.attemptMetrics {
		if !timed {
			lateNoTimer++
		}
	}
	s.mu.Unlock()
	s.d.mu.Lock()
	start, cancelAt, cancelStage := s.d.started, s.d.disconnectAt, s.d.stageAtCut
	s.d.mu.Unlock()
	var out RequestTiming
	if b != nil {
		start = b.Started()
		out.ClientFamily = observed(b.clientFamily)
		if b.Kind() == KindText {
			out.BudgetStartMode = observed(b.budgetStart)
			out.BudgetT0UnixMs = observed(start.UnixMilli())
			out.AnchorBasis = observed(b.AnchorBasis())
			out.AnchorEligible = observed(!eof.IsZero() && eof.Sub(start) >= BodyAnchorThreshold)
			out.FinalDeadlineMs = observed(b.Deadline().Sub(start).Milliseconds())
		}
	}
	if start.IsZero() {
		return out
	}
	offset := func(at time.Time) *int64 {
		if at.IsZero() {
			return nil
		}
		return observed(max(int64(0), at.Sub(start).Milliseconds()))
	}
	if !eof.IsZero() {
		out.BodyBytes = observed(bytes)
		out.ReceiptSource = observed(receiptSource)
	}
	out.FirstReadMs, out.BodyEOFMs = offset(firstRead), offset(eof)
	out.BodyReceiptRepeat, out.CDNObserved = observed(repeat), observed(edge)
	out.PreDispatchMs, out.KeepaliveCommitMs = offset(trace.firstDispatch), offset(trace.keepaliveCommit)
	if trace.wireStatus != 0 {
		out.WireStatus = observed(trace.wireStatus)
	}
	out.DispatchCount = observed(trace.dispatchCount)
	if trace.dispatchCount > 0 {
		out.MinDispatchRemainingMs = observed(trace.minRemaining.Milliseconds())
	}
	out.LateAttemptCount = observed(int64(lateCount))
	out.LateAttemptNoTimerCount = observed(lateNoTimer)
	out.ClientCancelMs = offset(cancelAt)
	if !cancelAt.IsZero() {
		out.CancelStage = observed(cancelStage)
	}
	return out
}

// TimingJSON includes response-stage diagnostics only in the ops JSON column.
func (s *RequestState) TimingJSON() *string {
	if s == nil {
		return nil
	}
	timing := s.Timing()
	s.mu.Lock()
	stage := s.timing.responseStage
	s.mu.Unlock()
	raw, err := json.Marshal(struct {
		RequestTiming
		ResponseStage           *ResponseStageTiming `json:"response_stage,omitempty"`
		LateAttemptCount        *int64               `json:"late_attempt_count,omitempty"`
		LateAttemptNoTimerCount *int64               `json:"late_attempt_no_timer_count,omitempty"`
	}{timing, stage, timing.LateAttemptCount, timing.LateAttemptNoTimerCount})
	if err != nil {
		return nil
	}
	return observed(string(raw))
}

func (b *Budget) AnchorBasis() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.anchor.Equal(b.started) {
		return BudgetStartBodyEOF
	}
	return BudgetStartArrival
}

func (s *RequestState) NoteKeepaliveCommit(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timing.keepaliveCommit.IsZero() {
		s.timing.keepaliveCommit = at
	}
}
func (s *RequestState) NoteWireStatus(status int) {
	if s == nil || status <= 0 {
		return
	}
	s.mu.Lock()
	s.timing.wireStatus = int64(status)
	s.mu.Unlock()
}
func (s *RequestState) NoteResponseStage(start time.Time, window time.Duration) {
	if s == nil {
		return
	}
	b := s.Budget()
	if b == nil {
		return
	}
	stage := &ResponseStageTiming{max(int64(0), start.Sub(b.Started()).Milliseconds()), window.Milliseconds()}
	s.mu.Lock()
	s.timing.responseStage = stage
	s.mu.Unlock()
}

// NoteAttemptTimer records observed coverage only. Creating a profile or plan
// does not count a dispatch, and later timers cannot rewrite an emitted sample.
func (s *RequestState) NoteAttemptTimer(at time.Time) {
	if s == nil || at.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.budget
	if b == nil || b.kind != KindText || at.Sub(b.started) <= b.base-minAttemptWindow {
		return
	}
	if s.timing.attemptTimers == nil {
		s.timing.attemptTimers = make(map[time.Time]bool)
	}
	s.timing.attemptTimers[at] = true
}

// ObserveAttemptStart publishes the late-attempt metric immediately before
// dispatch, after the caller has decided whether this attempt owns a timer.
// Profile preparation and Warm-only updates never emit a metric. In particular,
// a response-stage timer installed later cannot relabel the dispatch observation.
func (s *RequestState) ObserveAttemptStart() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.attemptProfile.timerOwnerStart()
	b := s.budget
	// These budget fields are immutable request-start snapshots.
	if b == nil || b.kind != KindText || owner.IsZero() || owner.Sub(b.started) <= b.base-minAttemptWindow {
		return
	}
	if _, emitted := s.timing.attemptMetrics[owner]; emitted {
		return
	}
	hasTimer := s.timing.attemptTimers[owner]
	if s.timing.attemptMetrics == nil {
		s.timing.attemptMetrics = make(map[time.Time]bool)
	}
	s.timing.attemptMetrics[owner] = hasTimer
	label := "false:" + s.budget.budgetStart
	if hasTimer {
		label = "true:" + s.budget.budgetStart
	}
	Count("late_attempt_start", s.groupID, 0, label)
}

// FinishTiming is retained for completion callers. Late-attempt metrics are
// already published at dispatch; completion must not count them again.
func (s *RequestState) FinishTiming() {}

// NoteOpenAIDispatch is called immediately before an actual HTTP transport.
// It observes only: it does not admit, consume an attempt, or change a deadline.
func (s *RequestState) NoteOpenAIDispatch(at time.Time, path string) {
	if s == nil {
		return
	}
	b := s.Budget()
	if b == nil || b.Kind() != KindText {
		return
	}
	rem := b.Remaining(at)
	// All OpenAI HTTP routes, including non-native protocol fallbacks, share
	// this existing transport observation point after their guard decision.
	s.ObserveAttemptStart()
	s.mu.Lock()
	if s.timing.dispatchCount == 0 {
		s.timing.firstDispatch, s.timing.minRemaining = at, rem
	}
	s.timing.dispatchCount++
	s.timing.minRemaining = min(s.timing.minRemaining, rem)
	bytes, committed := s.bodyBytes, !s.timing.keepaliveCommit.IsZero()
	s.mu.Unlock()
	ObserveDispatchRemaining(path, bytes, committed, rem, at)
}
