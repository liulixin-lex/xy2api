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
	Enabled         *bool   `json:"enabled,omitempty"`
	IntervalMinutes *int    `json:"interval_minutes,omitempty"`
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	OutputMode      *string `json:"output_mode,omitempty"`
}

var iqEffortPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func (s *IQCheckSettings) Validate() error {
	if s == nil {
		return nil
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
	if p == nil {
		return s
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
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	Model           string     `json:"model"`
	ReasoningEffort string     `json:"reasoning_effort"`
	OutputMode      string     `json:"output_mode"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	Revision        string     `json:"revision,omitempty"`
	LeaseToken      string     `json:"lease_token,omitempty"`
	LeaseUntil      *time.Time `json:"lease_until,omitempty"`
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
	s.Revision, s.LeaseToken, s.LeaseUntil = "", "", nil
	return s
}

func (s IQCheck) BlocksScheduling() bool { return s.Enabled && s.Status == "degraded" }

// CopySettings carries only editable configuration; duplicates and imports start disabled.
func (s IQCheck) CopySettings() *IQCheckSettings {
	s = s.WithSettings(nil)
	enabled := false
	return &IQCheckSettings{Enabled: &enabled, IntervalMinutes: &s.IntervalMinutes, Model: &s.Model, ReasoningEffort: &s.ReasoningEffort, OutputMode: &s.OutputMode}
}
