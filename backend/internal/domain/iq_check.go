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
		model := strings.TrimSpace(v.Model)
		if model == "" {
			model = IQModelAstra
		}
		var effort *string
		if v.ReasoningEffort != "" {
			effort = &v.ReasoningEffort
		}
		legacy := IQTargetSettings{Enabled: v.Enabled, ReasoningEffort: effort, IntervalMinutes: v.IntervalMinutes, TimeoutSeconds: v.TimeoutSeconds}
		for _, defaultModel := range DefaultIQModels {
			target := legacy
			target.Model = defaultModel
			p.Targets = append(p.Targets, target)
		}
		if model != IQModelAstra && model != IQModelSol {
			target := legacy
			target.Model = model
			p.Targets = append(p.Targets, target)
		}
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
	VerdictAt  *time.Time `json:"verdict_at"`
	LastRunAt  *time.Time `json:"last_run_at"`
	NextRunAt  *time.Time `json:"next_run_at"`
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
