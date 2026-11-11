//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProfitPreviewService(t *testing.T) {
	for _, tc := range []struct {
		name          string
		margin        float64
		wantPassing   int
		wantCeiling   float64
		probeAge      time.Duration
		mixedPlatform string
	}{
		{"request margin with zero buffer", 0.25, 3, 0.75, 0, ""},
		{"zero margin", 0, 4, 1, 0, ""},
		{"stricter margin", 0.9, 1, 0.1, 0, ""},
		{"fresh probe changes passing count", 0.25, 4, 0.75, time.Minute, ""},
		{"stale probe keeps stored rate", 0.25, 3, 0.75, 3 * time.Hour, ""},
		{"mixed anthropic members", 0.25, 3, 0.75, 0, PlatformAnthropic},
		{"mixed gemini members", 0.25, 3, 0.75, 0, PlatformGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := &Group{ID: 10, Platform: PlatformOpenAI, RateMultiplier: 1, ProfitMinMargin: 0.5, ProfitSafetyBuffer: 0.4}
			rows := make([]Account, 0, 7)
			for i, rate := range []float64{0, 0.3, 0.75, 0.9, -1} {
				a := healthyAccount(int64(i + 1))
				a.RateMultiplier = floatPtr(rate)
				rows = append(rows, a)
			}
			if tc.probeAge > 0 {
				probe := profitPreviewProbeAccount(4, UpstreamBillingProbeStatusOK, 0.1, time.Now().Add(-tc.probeAge), 30*time.Minute)
				rows[3].Type, rows[3].Extra = AccountTypeAPIKey, probe.Extra
			}
			disabled := healthyAccount(6)
			disabled.Schedulable = false
			inactive := healthyAccount(8)
			inactive.Status = StatusDisabled
			rows = append(rows, disabled, softAccount(7), inactive)
			if tc.mixedPlatform != "" {
				group.Platform = tc.mixedPlatform
				for i := range rows {
					rows[i].Platform = PlatformAntigravity
					rows[i].Extra = map[string]any{"mixed_scheduling": true}
				}
			}
			repo := &groupAccountsRepo{byGroup: map[int64][]Account{10: rows}}
			svc := NewProfitPreviewService(repo, &fallbackGroupRepo{groups: map[int64]*Group{10: group}})
			got, err := svc.ProfitControlPreview(context.Background(), 10, &tc.margin)
			require.NoError(t, err)
			require.True(t, repo.listedAll, "members query must include inactive accounts")
			require.Equal(t, 8, got.Members)
			require.Equal(t, 5, got.Schedulable)
			require.Equal(t, tc.wantPassing, got.Passing)
			require.InDelta(t, tc.wantCeiling, got.Ceiling, 1e-12)
			require.False(t, group.ProfitControlEnabled, "preview does not save the group")
			require.Equal(t, 0.4, group.ProfitSafetyBuffer)
		})
	}
}

func profitPreviewProbeAccount(id int64, status string, rate float64, receivedAt time.Time, interval time.Duration) *Account {
	return &Account{
		ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{
			UpstreamBillingProbeExtraKey: map[string]any{
				"status": status,
				"data": map[string]any{
					"billing_scope": "token", "resolved_rate_multiplier": rate,
					"peak_rate_enabled": false, "effective_rate_multiplier": rate,
				},
				"received_at":     receivedAt.UTC().Format(time.RFC3339Nano),
				"fresh_until":     receivedAt.Add(2 * interval).UTC().Format(time.RFC3339Nano),
				"last_attempt_at": receivedAt.UTC().Format(time.RFC3339Nano),
				"next_probe_at":   receivedAt.Add(interval).UTC().Format(time.RFC3339Nano),
			},
		},
	}
}
