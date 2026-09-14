package domain

import "time"

type IQCheckSettings struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
}

// IQCheck keeps the independent quality gate separate from manual scheduling.
type IQCheck struct {
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	Revision        string     `json:"revision,omitempty"`
	LeaseToken      string     `json:"lease_token,omitempty"`
	LeaseUntil      *time.Time `json:"lease_until,omitempty"`
}

func DefaultIQCheck() IQCheck { return IQCheck{IntervalMinutes: 15, Status: "unknown"} }

func (s IQCheck) Summary() IQCheck {
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
