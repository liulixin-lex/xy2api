package resilience

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Upstream balance handling keeps a switch and the pause length only
// (simplified for 0.4.0, 2026-10-17).

func TestBalanceSettingsKeepOnlyTheSwitchAndThePause(t *testing.T) {
	d := Defaults()
	require.True(t, d.Balance.Enabled)
	require.Equal(t, 300, d.Balance.PauseSeconds)

	raw, err := json.Marshal(d)
	require.NoError(t, err)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &doc))
	var balance map[string]any
	require.NoError(t, json.Unmarshal(doc["balance"], &balance))
	keys := make([]string, 0, len(balance))
	for k := range balance {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, []string{"enabled", "pause_seconds"}, keys)
}

func TestBalancePauseSecondsRange(t *testing.T) {
	for v, ok := range map[int]bool{29: false, 30: true, 300: true, 3600: true, 3601: false} {
		s := Defaults()
		s.Balance.PauseSeconds = v
		if ok {
			require.NoError(t, s.Validate(), v)
		} else {
			require.ErrorContains(t, s.Validate(), "balance.pause_seconds", v)
		}
	}
}

// A document stored by 0.4.0-rc.1 still carries the removed fields; it is
// read as before and the removed fields are ignored.
func TestParseIgnoresRemovedBalanceFields(t *testing.T) {
	s, err := Parse(`{"enabled":true,"balance":{"enabled":false,"recheck_schedule_seconds":[60,120,300,600,900],` +
		`"jitter_ratio":0.15,"suspect_ttl_seconds":90,"recovering_concurrency":1,"recovering_window_seconds":120,` +
		`"recovering_success_target":3,"generation_probe_daily_budget":48,"notify_after_hours":72}}`)
	require.NoError(t, err)
	require.False(t, s.Balance.Enabled)
	require.Equal(t, 300, s.Balance.PauseSeconds)
}

func TestBalancePauseReason(t *testing.T) {
	r := BalancePauseReason("HTTP 403: Insufficient account balance")
	require.Equal(t, "balance:exhausted[step=1]: HTTP 403: Insufficient account balance", r)
	require.True(t, IsBalancePause(r))
	require.Equal(t, TierHard, TempUnschedTier(r))
	require.LessOrEqual(t, len(BalancePauseReason(strings.Repeat("x", 500))), len("balance:exhausted[step=1]: ")+200)
	require.Equal(t, 1, BalancePauseStep("balance:exhausted: old reason [step=3]"))
	require.Equal(t, 2, BalancePauseStep("balance:exhausted[step=2]: reason"))
	require.Equal(t, 1, BalancePauseStep("balance:exhausted[step=9]: invalid"))
	for _, other := range []string{"transient:TRANSIENT HTTP 502", "OAuth 401: expired", "cn_balance_low: x", ""} {
		require.False(t, IsBalancePause(other), other)
	}
}

func TestBalancePauseStepAdvancesOnlyForPostExpiryAttempt(t *testing.T) {
	until := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	reason := "balance:exhausted[step=1]: prior failure"
	require.Equal(t, 1, BalancePauseStepForFailure(reason, until, until.Add(-time.Second), until.Add(time.Second)))
	require.Equal(t, 1, BalancePauseStepForFailure(reason, until, until, until.Add(time.Second)))
	require.Equal(t, 2, BalancePauseStepForFailure(reason, until, until.Add(time.Nanosecond), until.Add(time.Second)))
	require.Equal(t, 3, BalancePauseStepForFailure("balance:exhausted[step=3]: prior", until, until.Add(time.Second), until.Add(2*time.Second)))
}
