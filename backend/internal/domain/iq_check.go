package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	IQModelAstra    = "gpt-6-astra"
	IQModelSol      = "gpt-6.1-sol"
	DefaultIQModel  = IQModelAstra
	DefaultIQEffort = "low"
)

var DefaultIQModels = []string{IQModelAstra, IQModelSol}
var iqEffortPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

type IQTargetSettings struct {
	Model           string  `json:"model"`
	Enabled         *bool   `json:"enabled,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	IntervalMinutes *int    `json:"interval_minutes,omitempty"`
	TimeoutSeconds  *int    `json:"timeout_seconds,omitempty"`
}
type IQTargetsPatch struct {
	Targets []IQTargetSettings `json:"targets,omitempty"`
	Remove  []string           `json:"remove,omitempty"`
}

func (p *IQTargetsPatch) UnmarshalJSON(data []byte) error {
	var v struct {
		Targets         []IQTargetSettings `json:"targets"`
		Remove          []string           `json:"remove"`
		Enabled         *bool              `json:"enabled"`
		Model           string             `json:"model"`
		ReasoningEffort string             `json:"reasoning_effort"`
		IntervalMinutes *int               `json:"interval_minutes"`
		TimeoutSeconds  *int               `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	p.Targets, p.Remove = v.Targets, v.Remove
	if len(p.Targets) == 0 && (v.Enabled != nil || v.Model != "" || v.ReasoningEffort != "" || v.IntervalMinutes != nil || v.TimeoutSeconds != nil) {
		var effort *string
		if v.ReasoningEffort != "" {
			effort = &v.ReasoningEffort
		}
		p.Targets = []IQTargetSettings{{Model: v.Model, Enabled: v.Enabled, ReasoningEffort: effort, IntervalMinutes: v.IntervalMinutes, TimeoutSeconds: v.TimeoutSeconds}}
	}
	return nil
}

type IQTarget struct {
	ID                int64      `json:"id"`
	AccountID         int64      `json:"account_id"`
	Model             string     `json:"model"`
	Enabled           bool       `json:"enabled"`
	ReasoningEffort   string     `json:"reasoning_effort"`
	IntervalMinutes   int        `json:"interval_minutes"`
	TimeoutSeconds    int        `json:"timeout_seconds"`
	Revision          string     `json:"revision"`
	Verdict           string     `json:"verdict"`
	VerdictAt         *time.Time `json:"verdict_at,omitempty"`
	GateModel         string     `json:"gate_model,omitempty"`
	DegradedStreak    int        `json:"degraded_streak"`
	FailureStreak     int        `json:"failure_streak"`
	PausedReason      string     `json:"paused_reason"`
	LastRunAt         *time.Time `json:"last_run_at,omitempty"`
	LastResult        string     `json:"last_result"`
	LastReason        string     `json:"last_reason"`
	NextRunAt         time.Time  `json:"next_run_at"`
	NotBefore         *time.Time `json:"not_before,omitempty"`
	ManualRequestedAt *time.Time `json:"manual_requested_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
type IQTargetSummary struct {
	Model      string     `json:"model"`
	Verdict    string     `json:"verdict"`
	State      string     `json:"state"`
	Enabled    bool       `json:"enabled"`
	GateActive bool       `json:"gate_active"`
	VerdictAt  *time.Time `json:"verdict_at,omitempty"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty"`
}
type IQTargetView struct {
	Model             string     `json:"model"`
	Enabled           bool       `json:"enabled"`
	ReasoningEffort   string     `json:"reasoning_effort"`
	IntervalMinutes   int        `json:"interval_minutes"`
	TimeoutSeconds    int        `json:"timeout_seconds"`
	Verdict           string     `json:"verdict"`
	VerdictAt         *time.Time `json:"verdict_at"`
	GateModel         *string    `json:"gate_model"`
	GateActive        bool       `json:"gate_active"`
	GateResetAt       *time.Time `json:"gate_reset_at"`
	State             string     `json:"state"`
	PausedReason      string     `json:"paused_reason"`
	LastRunAt         *time.Time `json:"last_run_at"`
	LastResult        string     `json:"last_result"`
	LastReason        string     `json:"last_reason"`
	NextRunAt         *time.Time `json:"next_run_at"`
	NotBefore         *time.Time `json:"not_before"`
	RoundID           *int64     `json:"round_id"`
	CompletedAttempts int        `json:"completed_attempts"`
}
type IQProfile struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	OutputMode      string `json:"output_mode"`
}

func (p IQProfile) Defaults() IQProfile {
	if p.Model == "" {
		p.Model = IQModelAstra
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
	TimeoutSeconds  *int    `json:"timeout_seconds,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
	IntervalMinutes *int    `json:"interval_minutes,omitempty"`
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	OutputMode      *string `json:"output_mode,omitempty"`
}

func (s *IQCheckSettings) Validate() error {
	if s == nil {
		return nil
	}
	if s.TimeoutSeconds != nil && (*s.TimeoutSeconds < 30 || *s.TimeoutSeconds > 300) {
		return fmt.Errorf("timeout_seconds must be between 30 and 300")
	}
	if s.IntervalMinutes != nil && (*s.IntervalMinutes < 1 || *s.IntervalMinutes > 1440) {
		return fmt.Errorf("interval_minutes must be between 1 and 1440")
	}
	if s.Model != nil {
		m := strings.TrimSpace(*s.Model)
		if m == "" || len(m) > 256 || strings.IndexFunc(m, unicode.IsControl) >= 0 {
			return fmt.Errorf("model must contain 1 to 256 bytes without control characters")
		}
		*s.Model = m
	}
	if s.ReasoningEffort != nil {
		if err := ValidateIQEffort(*s.ReasoningEffort); err != nil {
			return err
		}
	}
	if s.OutputMode != nil && *s.OutputMode != "compat" && *s.OutputMode != "strict" {
		return fmt.Errorf("output_mode must be compat or strict")
	}
	return nil
}
func ValidateIQEffort(value string) error {
	if !iqEffortPattern.MatchString(strings.TrimSpace(value)) {
		return fmt.Errorf("invalid reasoning_effort")
	}
	return nil
}
func (s IQTargetSettings) Validate() error {
	model := strings.TrimSpace(s.Model)
	if model == "" || len(model) > 256 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return fmt.Errorf("model must contain 1 to 256 bytes without control characters")
	}
	if s.ReasoningEffort != nil {
		if err := ValidateIQEffort(*s.ReasoningEffort); err != nil {
			return err
		}
	}
	if s.IntervalMinutes != nil && (*s.IntervalMinutes < 1 || *s.IntervalMinutes > 1440) {
		return fmt.Errorf("interval_minutes must be between 1 and 1440")
	}
	if s.TimeoutSeconds != nil && (*s.TimeoutSeconds < 30 || *s.TimeoutSeconds > 300) {
		return fmt.Errorf("timeout_seconds must be between 30 and 300")
	}
	return nil
}

// IQCheck remains an opaque compatibility payload for the old account column.
type IQCheck struct {
	Raw               json.RawMessage `json:"-"`
	Enabled           bool            `json:"enabled"`
	IntervalMinutes   int             `json:"interval_minutes"`
	TimeoutSeconds    int             `json:"timeout_seconds"`
	Model             string          `json:"model"`
	ReasoningEffort   string          `json:"reasoning_effort"`
	OutputMode        string          `json:"output_mode"`
	Status            string          `json:"status"`
	Reason            string          `json:"reason,omitempty"`
	Revision          string          `json:"revision,omitempty"`
	LastValidStatus   string          `json:"last_valid_status,omitempty"`
	LastValidReason   string          `json:"last_valid_reason,omitempty"`
	LastValidAt       *time.Time      `json:"last_valid_at,omitempty"`
	LastRunStatus     string          `json:"last_run_status,omitempty"`
	LastRunReason     string          `json:"last_run_reason,omitempty"`
	LastRunAt         *time.Time      `json:"last_run_at,omitempty"`
	LastAttemptAt     *time.Time      `json:"last_attempt_at,omitempty"`
	NextRunAt         *time.Time      `json:"next_run_at,omitempty"`
	LeaseToken        string          `json:"lease_token,omitempty"`
	LeaseUntil        *time.Time      `json:"lease_until,omitempty"`
	NotBefore         *time.Time      `json:"not_before,omitempty"`
	ExecutionState    string          `json:"execution_state,omitempty"`
	ExecutionReason   string          `json:"execution_reason,omitempty"`
	RoundID           string          `json:"round_id,omitempty"`
	RoundStartedAt    *time.Time      `json:"round_started_at,omitempty"`
	RoundDeadline     *time.Time      `json:"round_deadline,omitempty"`
	RetryAt           *time.Time      `json:"retry_at,omitempty"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	AttemptCount      int             `json:"attempt_count,omitempty"`
	TaskID            string          `json:"task_id,omitempty"`
	NextEligibleAt    *time.Time      `json:"next_eligible_at,omitempty"`
	BusyDeferrals     int             `json:"busy_deferrals,omitempty"`
	FailureStreak     int             `json:"failure_streak,omitempty"`
	ProtocolFailures  int             `json:"protocol_failures,omitempty"`
	SchedulingBlocked bool            `json:"scheduling_blocked,omitempty"`
}

// Legacy execution fields remain readable until the account-level worker is removed.
func (s IQCheck) taskID() string { return "" }
func (s *IQCheck) UnmarshalJSON(data []byte) error {
	type plain IQCheck
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = IQCheck(v)
	s.Raw = append(s.Raw[:0], data...)
	return nil
}
func (s IQCheck) MarshalJSON() ([]byte, error) { type plain IQCheck; return json.Marshal(plain(s)) }
func DefaultIQCheck() IQCheck {
	return IQCheck{Enabled: false, IntervalMinutes: 5, TimeoutSeconds: 120, Model: IQModelAstra, ReasoningEffort: DefaultIQEffort, OutputMode: "compat", Status: "unknown"}
}
func (s IQCheck) Defaults() IQCheck {
	if s.IntervalMinutes == 0 {
		s.IntervalMinutes = 5
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 120
	}
	if s.Model == "" {
		s.Model = IQModelAstra
	}
	if s.ReasoningEffort == "" {
		s.ReasoningEffort = DefaultIQEffort
	}
	if s.OutputMode == "" {
		s.OutputMode = "compat"
	}
	if s.Status == "" {
		s.Status = "unknown"
	}
	return s
}
func (s IQCheck) Profile() IQProfile {
	s = s.Defaults()
	return IQProfile{Model: s.Model, ReasoningEffort: s.ReasoningEffort, OutputMode: s.OutputMode}
}
func (s IQCheck) WithSettings(p *IQCheckSettings) IQCheck {
	s = s.Defaults()
	if p == nil {
		return s
	}
	if p.TimeoutSeconds != nil {
		s.TimeoutSeconds = *p.TimeoutSeconds
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
func (s IQCheck) CurrentAssessment() IQCheck {
	if s.LastValidStatus == "" && (s.Status == "smart" || s.Status == "degraded") {
		s.LastValidStatus = s.Status
		s.LastValidReason = s.Reason
		if s.LastValidAt == nil {
			s.LastValidAt = s.LastRunAt
		}
	}
	if s.LastRunStatus == "unknown" {
		s.Status = "unknown"
		s.Reason = s.LastRunReason
	}
	return s
}
func (s *IQCheck) ObserveResult(status, reason string, now time.Time) {
	if status != "smart" && status != "degraded" {
		status = "unknown"
	}
	s.Status, s.Reason, s.LastRunStatus, s.LastRunReason, s.LastRunAt = status, reason, status, reason, &now
	if status == "smart" || status == "degraded" {
		s.LastValidStatus, s.LastValidReason, s.LastValidAt = status, reason, &now
	}
}
func (s IQCheck) BlocksScheduling() bool {
	return s.Enabled && s.CurrentAssessment().LastValidStatus == "degraded"
}
func (s IQCheck) SchedulingBlockedValue() bool { return s.BlocksScheduling() }
func (s IQCheck) Summary() IQCheck {
	s = s.Defaults()
	s.LeaseToken = ""
	s.LeaseUntil = nil
	s.RoundID = ""
	return s
}
func (s IQCheck) CopySettings() *IQCheckSettings {
	s = s.Defaults()
	e := false
	return &IQCheckSettings{Enabled: &e, IntervalMinutes: &s.IntervalMinutes, TimeoutSeconds: &s.TimeoutSeconds, Model: &s.Model, ReasoningEffort: &s.ReasoningEffort, OutputMode: &s.OutputMode}
}
func (s IQCheck) EffectiveInterval() time.Duration {
	return time.Duration(s.Defaults().IntervalMinutes) * time.Minute
}
func (s IQCheck) Eligibility(now time.Time) (time.Time, string) {
	if s.NextRunAt != nil && s.NextRunAt.After(now) {
		return *s.NextRunAt, "minimum_interval"
	}
	if s.LastRunAt != nil {
		t := s.LastRunAt.Add(s.EffectiveInterval())
		if t.After(now) {
			return t, "minimum_interval"
		}
	}
	return now, ""
}
