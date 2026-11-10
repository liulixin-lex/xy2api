//go:build unit

package service

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestAvailabilityFallbackReason(t *testing.T) {
	paused := healthyAccount(1)
	until := time.Now().Add(time.Minute)
	paused.TempUnschedulableUntil = &until
	paused.TempUnschedulableReason = resilience.BalancePauseReason("HTTP 402")
	expired := paused
	past := time.Now().Add(-time.Minute)
	expired.TempUnschedulableUntil = &past
	expired.RateLimitResetAt = &until
	disabled := paused
	disabled.Schedulable = false
	for _, tc := range []struct {
		name     string
		accounts []Account
		fallback bool
		reason   string
	}{
		{"balance", []Account{paused}, true, "balance_exhausted"},
		{"mixed blockers", []Account{softAccount(3), paused}, true, "balance_exhausted"},
		{"soft only", []Account{softAccount(3)}, true, "no_servable_account"},
		{"expired balance", []Account{expired}, true, "no_servable_account"},
		{"disabled balance", []Account{disabled}, true, "no_servable_account"},
		{"empty group", nil, true, "no_servable_account"},
		{"healthy member", []Account{paused, healthyAccount(3)}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := slog.Default()
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			r, _, accounts, key := newFallbackFixture(t)
			accounts.byGroup[10] = tc.accounts
			got := r.AvailabilityFallbackGroup(context.Background(), key)
			if !tc.fallback {
				require.Nil(t, got)
				require.Empty(t, logs.String())
				return
			}
			require.NotNil(t, got)
			require.Equal(t, int64(20), got.ID)
			require.Contains(t, logs.String(), "reason="+tc.reason)
			logs.Reset()
			require.NotNil(t, r.AvailabilityFallbackGroup(context.Background(), key))
			require.Empty(t, logs.String(), "remembered fallback must not log again")
		})
	}
}
