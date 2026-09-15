package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const DefaultIQModel = "gpt-6-astra"
const DefaultIQEffort = "low"

type IQProfile struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	OutputMode      string `json:"output_mode"`
}

func (p IQProfile) Defaults() IQProfile {
	if p.Model == "" {
		p.Model = DefaultIQModel
	}
	if p.ReasoningEffort == "" {
		p.ReasoningEffort = DefaultIQEffort
	}
	if p.OutputMode == "" {
		p.OutputMode = "compat"
	}
	return p
}

type IQCheckSettings struct {
	QuotaGroup         *string `json:"quota_group,omitempty"`
	TimeoutSeconds     *int    `json:"timeout_seconds,omitempty"`
	DailyRequestLimit  *int    `json:"daily_request_limit,omitempty"`
	MaxIntervalMinutes *int    `json:"max_interval_minutes,omitempty"`
	SchedulingMode     *string `json:"scheduling_mode,omitempty"`
	Enabled            *bool   `json:"enabled,omitempty"`
	IntervalMinutes    *int    `json:"interval_minutes,omitempty"`
	Model              *string `json:"model,omitempty"`
	ReasoningEffort    *string `json:"reasoning_effort,omitempty"`
	OutputMode         *string `json:"output_mode,omitempty"`
}

var iqEffortPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func (s *IQCheckSettings) Validate() error {
	if s == nil {
		return nil
	}

	if s.SchedulingMode != nil && *s.SchedulingMode != "fixed" && *s.SchedulingMode != "adaptive" {
		return fmt.Errorf("scheduling_mode must be fixed or adaptive")
	}
	if s.MaxIntervalMinutes != nil && (*s.MaxIntervalMinutes < 1 || *s.MaxIntervalMinutes > 1440) {
		return fmt.Errorf("max_interval_minutes must be between 1 and 1440")
	}
	if s.DailyRequestLimit != nil && (*s.DailyRequestLimit < 1 || *s.DailyRequestLimit > 1440) {
		return fmt.Errorf("daily_request_limit must be between 1 and 1440")
	}
	if s.TimeoutSeconds != nil && (*s.TimeoutSeconds < 30 || *s.TimeoutSeconds > 300) {
		return fmt.Errorf("timeout_seconds must be between 30 and 300")
	}
	if s.QuotaGroup != nil && (*s.QuotaGroup != "" && !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(*s.QuotaGroup)) {
		return fmt.Errorf("invalid quota_group")
	}
	if s.IntervalMinutes != nil && (*s.IntervalMinutes < 1 || *s.IntervalMinutes > 1440) {
		return fmt.Errorf("interval_minutes must be between 1 and 1440")
	}
	if s.Model != nil {
		*s.Model = strings.TrimSpace(*s.Model)
		if *s.Model == "" || len(*s.Model) > 256 || strings.IndexFunc(*s.Model, unicode.IsControl) >= 0 {
			return fmt.Errorf("model must contain 1 to 256 bytes without control characters")
		}
	}
	if s.ReasoningEffort != nil {
		*s.ReasoningEffort = strings.TrimSpace(*s.ReasoningEffort)
		if !iqEffortPattern.MatchString(*s.ReasoningEffort) {
			return fmt.Errorf("invalid reasoning_effort")
		}
	}
	if s.OutputMode != nil && *s.OutputMode != "compat" && *s.OutputMode != "strict" {
		return fmt.Errorf("output_mode must be compat or strict")
	}
	return nil
}

func (s IQCheck) Profile() IQProfile {
	return (IQProfile{s.Model, s.ReasoningEffort, s.OutputMode}).Defaults()
}

func (s IQCheck) WithSettings(p *IQCheckSettings) IQCheck {
	profile := s.Profile()
	s.Model, s.ReasoningEffort, s.OutputMode = profile.Model, profile.ReasoningEffort, profile.OutputMode
	if s.IntervalMinutes == 0 {
		s.IntervalMinutes = 15
	}
	if s.SchedulingMode == "" {
		s.SchedulingMode = "fixed"
	}
	if s.MaxIntervalMinutes == 0 {
		s.MaxIntervalMinutes = 60
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 120
	}
	if p == nil {
		return s
	}
	if p.SchedulingMode != nil {
		s.SchedulingMode = *p.SchedulingMode
	}
	if p.MaxIntervalMinutes != nil {
		s.MaxIntervalMinutes = *p.MaxIntervalMinutes
	}
	if p.DailyRequestLimit != nil {
		s.DailyRequestLimit = *p.DailyRequestLimit
	}
	if p.TimeoutSeconds != nil {
		s.TimeoutSeconds = *p.TimeoutSeconds
	}
	if p.QuotaGroup != nil {
		s.QuotaGroup = *p.QuotaGroup
	}
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	if p.IntervalMinutes != nil {
		s.IntervalMinutes = *p.IntervalMinutes
	}
	if p.Model != nil {
		s.Model = *p.Model
	}
	if p.ReasoningEffort != nil {
		s.ReasoningEffort = *p.ReasoningEffort
	}
	if p.OutputMode != nil {
		s.OutputMode = *p.OutputMode
	}
	return s
}

// IQCheck keeps the independent quality gate separate from manual scheduling.
type IQCheck struct {
	BusyDeferrals    int        `json:"busy_deferrals,omitempty"`
	LastAttemptAt    *time.Time `json:"last_attempt_at,omitempty"`
	ExecutionState   string     `json:"execution_state,omitempty"`
	ExecutionReason  string     `json:"execution_reason,omitempty"`
	TaskID           string     `json:"task_id,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	NotBefore        *time.Time `json:"not_before,omitempty"`
	SmartStreak      int        `json:"smart_streak,omitempty"`
	FailureStreak    int        `json:"failure_streak,omitempty"`
	ProtocolFailures int        `json:"protocol_failures,omitempty"`
	BudgetDay        string     `json:"budget_day,omitempty"`
	BudgetUsed       int        `json:"budget_used"`
	BudgetRemaining  int        `json:"budget_remaining"`
	Freshness        string     `json:"freshness,omitempty"`
	NextEligibleAt   *time.Time `json:"next_eligible_at,omitempty"`

	QuotaGroup         string     `json:"quota_group"`
	TimeoutSeconds     int        `json:"timeout_seconds"`
	DailyRequestLimit  int        `json:"daily_request_limit"`
	MaxIntervalMinutes int        `json:"max_interval_minutes"`
	SchedulingMode     string     `json:"scheduling_mode"`
	Enabled            bool       `json:"enabled"`
	IntervalMinutes    int        `json:"interval_minutes"`
	Model              string     `json:"model"`
	ReasoningEffort    string     `json:"reasoning_effort"`
	OutputMode         string     `json:"output_mode"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	LastRunAt          *time.Time `json:"last_run_at,omitempty"`
	NextRunAt          *time.Time `json:"next_run_at,omitempty"`
	Revision           string     `json:"revision,omitempty"`
	LeaseToken         string     `json:"lease_token,omitempty"`
	LeaseUntil         *time.Time `json:"lease_until,omitempty"`
}

func DefaultIQCheck() IQCheck {
	return (IQCheck{IntervalMinutes: 15, Status: "unknown"}).WithSettings(nil)
}

func (s IQCheck) Summary() IQCheck {
	s = s.WithSettings(nil)
	if s.IntervalMinutes == 0 {
		s.IntervalMinutes = 15
	}
	if s.Status == "" || !s.Enabled {
		s.Status = "unknown"
	}

	now := time.Now().UTC()
	s.Freshness = "never_checked"
	if s.LastRunAt != nil {
		s.Freshness = "fresh"
		if now.After(s.LastRunAt.Add(s.EffectiveInterval() + time.Duration(s.TimeoutSeconds)*time.Second + time.Minute)) {
			s.Freshness = "stale"
		}
	}
	used := s.BudgetUsed
	if s.BudgetDay != now.Format("2006-01-02") {
		used = 0
	}
	s.BudgetRemaining = max(0, s.DailyLimit()-used)
	s.NextEligibleAt = s.NextRunAt
	if s.ExecutionState == "" {
		s.ExecutionState = "idle"
	}
	s.Revision, s.LeaseToken, s.LeaseUntil = "", "", nil
	return s
}

func (s IQCheck) BlocksScheduling() bool { return s.Enabled && s.Status == "degraded" }

// CopySettings carries only editable configuration; duplicates and imports start disabled.
func (s IQCheck) CopySettings() *IQCheckSettings {
	s = s.WithSettings(nil)
	enabled := false
	return &IQCheckSettings{SchedulingMode: &s.SchedulingMode, MaxIntervalMinutes: &s.MaxIntervalMinutes, DailyRequestLimit: iqCopyBudget(s), TimeoutSeconds: &s.TimeoutSeconds, QuotaGroup: &s.QuotaGroup, Enabled: &enabled, IntervalMinutes: &s.IntervalMinutes, Model: &s.Model, ReasoningEffort: &s.ReasoningEffort, OutputMode: &s.OutputMode}
}

func iqCopyBudget(s IQCheck) *int {
	if s.DailyRequestLimit == 0 {
		return nil
	}
	return &s.DailyRequestLimit
}
func (s IQCheck) DailyLimit() int {
	if s.DailyRequestLimit > 0 {
		return s.DailyRequestLimit
	}
	return (1440 + max(1, s.IntervalMinutes) - 1) / max(1, s.IntervalMinutes)
}
func (s IQCheck) EffectiveInterval() time.Duration {
	s = s.WithSettings(nil)
	n := s.IntervalMinutes
	if s.SchedulingMode == "adaptive" {
		if s.SmartStreak >= 6 {
			n *= 4
		} else if s.SmartStreak >= 3 {
			n *= 2
		}
		n = min(n, max(s.IntervalMinutes, s.MaxIntervalMinutes))
	}
	return time.Duration(n) * time.Minute
}

// Eligibility is independent of the assessment and survives toggles and profile edits.
func (s IQCheck) Eligibility(now time.Time) (time.Time, string) {
	next := now
	reason := ""
	if s.LastRunAt != nil {
		t := s.LastRunAt.Add(time.Duration(max(1, s.IntervalMinutes)) * time.Minute)
		if t.After(next) {
			next = t
			reason = "minimum_interval"
		}
	}
	if s.LastAttemptAt != nil {
		t := s.LastAttemptAt.Add(time.Duration(max(1, s.IntervalMinutes)) * time.Minute)
		if t.After(next) {
			next, reason = t, "minimum_interval"
		}
	}
	if s.NotBefore != nil && s.NotBefore.After(next) {
		next = *s.NotBefore
		reason = "upstream_cooldown"
	}
	if s.BudgetDay == now.UTC().Format("2006-01-02") && s.BudgetUsed >= s.DailyLimit() {
		t := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
		if t.After(next) {
			next = t
			reason = "daily_budget"
		}
	}
	return next, reason
}
