package resilience

// TimeoutDiagnostics is populated only by a fired FOT timer. Attribution off
// leaves would/applied false; omitempty excludes their zero values.
type TimeoutDiagnostics struct {
	TimeoutSource            string `json:"timeout_source,omitempty"`
	TierMs                   int64  `json:"tier_ms,omitempty"`
	PlannedTimeoutMs         int64  `json:"planned_timeout_ms,omitempty"`
	EffectiveTimeoutMs       int64  `json:"effective_timeout_ms,omitempty"`
	BudgetRemainingMsAtStart int64  `json:"budget_remaining_ms_at_start,omitempty"`
	WouldBudgetCut           bool   `json:"would_budget_cut,omitempty"`
	BudgetCutApplied         bool   `json:"budget_cut_applied,omitempty"`
	CutAfterFullTier         bool   `json:"budget_cut_after_full_tier,omitempty"`
	AnchorOffsetMs           int64  `json:"anchor_offset_ms,omitempty"`
	AttemptStartOffsetMs     int64  `json:"attempt_start_offset_ms,omitempty"`
	CommittedAtStart         bool   `json:"committed_at_start,omitempty"`
	GuardDisarmMs            int64  `json:"guard_disarm_ms,omitempty"`
	ShadowEOFRemainingMs     int64  `json:"shadow_eof_remaining_ms,omitempty"`
	TTFOLowerBoundMs         int64  `json:"ttfo_lower_bound_ms,omitempty"`
}
