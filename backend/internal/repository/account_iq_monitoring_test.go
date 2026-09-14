package repository

import (
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIQMonitoringSchedule(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	s := domain.DefaultIQCheck()
	s.Enabled = true
	s.SchedulingMode = "adaptive"
	for i := 1; i <= 6; i++ {
		finishIQSchedule(&s, iqcheck.Grade("21"), now)
		want := 15 * time.Minute
		if i >= 3 {
			want = 30 * time.Minute
		}
		if i >= 6 {
			want = time.Hour
		}
		require.Equal(t, want, s.NextRunAt.Sub(now))
		now = *s.NextRunAt
	}
	finishIQSchedule(&s, iqcheck.Grade("29"), now)
	require.True(t, s.BlocksScheduling())
	require.Equal(t, 15*time.Minute, s.NextRunAt.Sub(now))
	for i := 1; i <= 3; i++ {
		finishIQSchedule(&s, iqcheck.Unknown("timeout"), now)
		require.Equal(t, time.Duration(1<<(i-1))*15*time.Minute, s.NextRunAt.Sub(now))
		require.False(t, s.BlocksScheduling())
	}
	after := now.Add(24 * time.Hour)
	r := iqcheck.Unknown("http_429")
	r.Diagnostic = &iqcheck.Diagnostic{RetryAfter: &after}
	finishIQSchedule(&s, r, now)
	require.Equal(t, after, *s.NextRunAt)
	finishIQSchedule(&s, iqcheck.Unknown("quota_exhausted"), now)
	require.Equal(t, "paused", s.ExecutionState)
	require.Nil(t, s.NextRunAt)
	queueIQ(&s, now)
	require.Equal(t, "deferred", s.ExecutionState)
	require.Equal(t, after, *s.NextRunAt)
	r = iqcheck.Unknown("invalid_event_json")
	r.Diagnostic = &iqcheck.Diagnostic{Stage: "parse"}
	s.NotBefore = nil
	for i := 0; i < 3; i++ {
		finishIQSchedule(&s, r, now)
	}
	require.Equal(t, "paused", s.ExecutionState)
}
func TestIQMonitoringSettingsPreserveGateAndBudget(t *testing.T) {
	now := time.Now().UTC()
	s := domain.DefaultIQCheck()
	s.Enabled = true
	s.Status = "degraded"
	s.BudgetDay = now.Format("2006-01-02")
	s.BudgetUsed = 96
	s.LastRunAt = &now
	delay := now.Add(2 * time.Hour)
	s.NotBefore = &delay
	n := 30
	u := applyIQSettings(s, &domain.IQCheckSettings{IntervalMinutes: &n}, false, now)
	require.True(t, u.BlocksScheduling())
	require.Equal(t, 96, u.BudgetUsed)
	timeout := 300
	u = applyIQSettings(s, &domain.IQCheckSettings{TimeoutSeconds: &timeout}, false, now)
	require.Equal(t, "unknown", u.Status)
	require.Equal(t, 96, u.BudgetUsed)
	require.False(t, u.NextRunAt.Before(delay))
}
