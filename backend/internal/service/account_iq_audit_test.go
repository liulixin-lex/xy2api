package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAccountIQAuditSchedulingGate(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	constraints := []struct {
		name  string
		apply func(*Account)
	}{
		{"manual_disabled", func(a *Account) { a.Schedulable = false }},
		{"inactive", func(a *Account) { a.Status = "inactive" }},
		{"expired", func(a *Account) {
			a.AutoPauseOnExpired = true
			a.ExpiresAt = &past
		}},
		{"overloaded", func(a *Account) { a.OverloadUntil = &future }},
		{"rate_limited", func(a *Account) { a.RateLimitResetAt = &future }},
		{"temporarily_unschedulable", func(a *Account) { a.TempUnschedulableUntil = &future }},
	}
	tests := []struct {
		name    string
		iqCheck domain.IQCheck
		want    bool
	}{
		{
			name:    "enabled_degraded_blocked",
			iqCheck: domain.IQCheck{Enabled: true, Status: "degraded"},
			want:    false,
		},
		{
			name:    "disabled_degraded_allowed",
			iqCheck: domain.IQCheck{Enabled: false, Status: "degraded"},
			want:    true,
		},
		{
			name:    "enabled_unknown_allowed",
			iqCheck: domain.IQCheck{Enabled: true, Status: "unknown"},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := Account{
				Platform:    PlatformOpenAI,
				Status:      StatusActive,
				Schedulable: true,
				IQCheck:     tt.iqCheck,
			}
			require.Equal(t, tt.want, account.IsSchedulable())
			if !tt.want {
				return
			}
			for _, constraint := range constraints {
				t.Run(constraint.name, func(t *testing.T) {
					blocked := account
					constraint.apply(&blocked)
					require.False(t, blocked.IsSchedulable())
				})
			}
		})
	}
}
