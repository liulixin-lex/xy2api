package resilience

import "time"

// TimeoutPlan belongs to one concrete timer. Private fields and value passing
// prevent later profile/tier changes from altering a fired timer's attribution.
type TimeoutPlan struct {
	testTrace                                  timeoutPlanTrace
	seq                                        uint64
	accountID                                  int64
	model                                      string
	start                                      time.Time
	tier, planned, effective, remaining        time.Duration
	source, attribution                        string
	would, fullTier, applied, committed        bool
	anchorOffset, startOffset, shadowRemaining time.Duration
}

func (p TimeoutPlan) Effective() time.Duration { return p.effective }
func (p TimeoutPlan) Start() time.Time         { return p.start }
func (p TimeoutPlan) Applied() bool            { return p.applied }
func (p TimeoutPlan) Model() string            { return p.model }
func (p TimeoutPlan) Diagnostics() TimeoutDiagnostics {
	if p.source == "" {
		return TimeoutDiagnostics{}
	}
	return TimeoutDiagnostics{TimeoutSource: p.source, TierMs: p.tier.Milliseconds(), PlannedTimeoutMs: p.planned.Milliseconds(), EffectiveTimeoutMs: p.effective.Milliseconds(), BudgetRemainingMsAtStart: p.remaining.Milliseconds(), WouldBudgetCut: p.would, BudgetCutApplied: p.applied, CutAfterFullTier: p.would && p.fullTier, AnchorOffsetMs: p.anchorOffset.Milliseconds(), AttemptStartOffsetMs: p.startOffset.Milliseconds(), CommittedAtStart: p.committed, ShadowEOFRemainingMs: p.shadowRemaining.Milliseconds(), TTFOLowerBoundMs: p.effective.Milliseconds()}
}

// budgetCutApplied derives the frozen plan decision. The advanced
// scheduler's existing ReportResult/ShouldReportAccountScheduleFailure is NOT
// exempted in phase one; a later PM ruling can extend this decision centrally.
func budgetCutApplied(would, fullTier bool, attribution string) bool {
	return would && !fullTier && attribution == BudgetAttributionEnforce
}
func (b *Budget) PlanAttemptTimeout(tier time.Duration, last bool, start time.Time) TimeoutPlan {
	if b == nil {
		return TimeoutPlan{effective: tier}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attemptStarted = true
	b.planSequence++
	rem := time.Duration(0)
	if !b.deadline.IsZero() {
		rem = max(time.Nanosecond, b.deadline.Sub(start))
	}
	planned := tier
	if spent := start.Sub(b.started); tier > 0 && spent > tier {
		planned = spent
	}
	effective := planned
	source := "tier"
	if planned > tier {
		source = "raised_spent"
	}
	if last {
		effective = rem
		source = "last_candidate"
		if rem < planned {
			source = "last_candidate_budget"
		}
	} else if rem > 0 && (planned <= 0 || rem < planned) {
		effective = rem
		source = "remaining_cap"
	}
	p := TimeoutPlan{seq: b.planSequence, start: start, tier: tier, planned: planned, effective: effective, remaining: rem, source: source, attribution: b.attribution, anchorOffset: b.anchor.Sub(b.started), startOffset: start.Sub(b.started)}
	// PM revision 10: construction-time fair window, never trigger-time elapsed.
	p.fullTier = tier > 0 && rem >= tier
	if b.attribution != BudgetAttributionOff {
		p.would = tier > 0 && (source == "remaining_cap" || source == "last_candidate_budget")
		p.applied = budgetCutApplied(p.would, p.fullTier, b.attribution)
	}
	if b.attribution == BudgetAttributionObserve && !b.bodyReceived.IsZero() {
		shadowAnchor := b.started
		if b.bodyReceived.Sub(b.started) >= BodyAnchorThreshold {
			shadowAnchor = b.bodyReceived
		}
		shadowDeadline := shadowAnchor.Add(b.base)
		if cap := b.started.Add(PreOutputWallCap); cap.Before(shadowDeadline) {
			shadowDeadline = cap
		}
		p.shadowRemaining = max(time.Nanosecond, shadowDeadline.Sub(start))
	}
	return p
}
func (s *RequestState) PlanAttemptTimeout(tier time.Duration, last bool, start time.Time) TimeoutPlan {
	b := s.Budget()
	p := b.PlanAttemptTimeout(tier, last, start)
	if s == nil {
		return p
	}
	s.mu.Lock()
	p.committed = !s.timing.keepaliveCommit.IsZero()
	p.accountID = s.attemptProfile.AccountID
	p.model = s.attemptProfile.Model
	if BudgetTraceEnabledForTest {
		captureTimeoutPlanForTest(b, &p, s.attemptProfile, s)
	}
	s.mu.Unlock()
	s.NoteAttemptTimer(start)
	return p
}

// ResponseStagePlan has no tier and never participates in cut attribution.
func (s *RequestState) ResponseStagePlan(start time.Time) TimeoutPlan {
	b := s.Budget()
	if b == nil {
		return TimeoutPlan{}
	}
	b.MarkAttemptStarted()
	p := TimeoutPlan{start: start, effective: b.Remaining(start), source: "response_stage_remaining", anchorOffset: b.DeadlineAnchor().Sub(b.Started()), startOffset: start.Sub(b.Started())}
	p.remaining = p.effective
	s.mu.Lock()
	p.committed = !s.timing.keepaliveCommit.IsZero()
	p.accountID = s.attemptProfile.AccountID
	p.model = s.attemptProfile.Model
	if BudgetTraceEnabledForTest {
		captureTimeoutPlanForTest(b, &p, s.attemptProfile, s)
	}
	s.mu.Unlock()
	s.NoteAttemptTimer(s.AttemptProfile().AttemptStart)
	return p
}
