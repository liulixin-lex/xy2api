package resilience

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func noJitter() Settings {
	s := Defaults()
	s.Balance.JitterRatio = 0
	return s
}

func TestBalanceRecordRoundTrip(t *testing.T) {
	r := BalanceRecord{State: BalanceExhausted, Since: t0, NextCheckAt: t0.Add(time.Minute), Level: 2, LastEvidence: "x"}
	got := BalanceRecordFrom(r.Extra())
	require.Equal(t, r.State, got.State)
	require.True(t, r.Since.Equal(got.Since))
	require.True(t, r.NextCheckAt.Equal(got.NextCheckAt))
	require.Equal(t, 2, got.Level)

	require.Equal(t, BalanceOK, BalanceRecordFrom(nil).State)
	require.Equal(t, BalanceOK, BalanceRecordFrom(map[string]any{BalanceExtraKey: "garbage"}).State)
}

func TestStrongSignalExhausts(t *testing.T) {
	// Spec: Claude relay runs out of credit -> EXHAUSTED.
	s := noJitter()
	r := OnStrongSignal(BalanceRecord{State: BalanceOK}, &s, "403 insufficient balance", t0)
	require.Equal(t, BalanceExhausted, r.State)
	require.Equal(t, 0, r.Level)
	require.Equal(t, t0.Add(60*time.Second), r.NextCheckAt)
	require.True(t, strings.HasPrefix(r.TempUnschedReason(), ReasonPrefixBalanceExhausted))
	require.Equal(t, t0.Add(60*time.Second+10*time.Minute), r.ExclusionUntil(&s))
	require.Equal(t, TierHard, TempUnschedTier(r.TempUnschedReason()))

	// Repeated strong signals keep the schedule.
	again := OnStrongSignal(r, &s, "again", t0.Add(10*time.Second))
	require.Equal(t, r.NextCheckAt, again.NextCheckAt)
	require.Equal(t, "again", again.LastEvidence)
}

func TestRecheckScheduleAndBackoff(t *testing.T) {
	s := noJitter()
	r := OnStrongSignal(BalanceRecord{}, &s, "e", t0)
	want := []time.Duration{120, 300, 600, 900, 900, 900}
	now := t0
	for _, w := range want {
		now = r.NextCheckAt
		require.True(t, r.Due(now))
		require.False(t, r.Due(now.Add(-time.Second)))
		r = OnProbe(r, &s, ProbeNegative, "usage", "balance 0", now)
		require.Equal(t, BalanceExhausted, r.State)
		require.Equal(t, now.Add(w*time.Second), r.NextCheckAt)
	}
}

func TestProbePositiveRecovers(t *testing.T) {
	// Spec: automatic recovery after top-up.
	s := noJitter()
	r := OnStrongSignal(BalanceRecord{}, &s, "e", t0)
	r = OnProbe(r, &s, ProbePositive, "usage", "balance 12.5", t0.Add(time.Minute))
	require.Equal(t, BalanceRecovering, r.State)
	require.Equal(t, "", r.TempUnschedReason())
	require.True(t, r.ExclusionUntil(&s).IsZero())

	var changed bool
	r, changed = OnSuccess(r, &s, t0.Add(61*time.Second))
	require.True(t, changed)
	require.Equal(t, BalanceRecovering, r.State)
	r, _ = OnSuccess(r, &s, t0.Add(62*time.Second))
	require.Equal(t, BalanceRecovering, r.State)
	r, _ = OnSuccess(r, &s, t0.Add(63*time.Second))
	require.Equal(t, BalanceOK, r.State)
}

func TestRecoveringWindowElapses(t *testing.T) {
	s := noJitter()
	r := OnProbe(OnStrongSignal(BalanceRecord{}, &s, "e", t0), &s, ProbePositive, "usage", "", t0)
	r, _ = OnSuccess(r, &s, t0.Add(121*time.Second))
	require.Equal(t, BalanceOK, r.State)
}

func TestFailureWhileRecoveringBacksOff(t *testing.T) {
	s := noJitter()
	r := OnStrongSignal(BalanceRecord{}, &s, "e", t0)
	r = OnProbe(r, &s, ProbeNegative, "usage", "", r.NextCheckAt) // level 1
	r = OnProbe(r, &s, ProbePositive, "usage", "", r.NextCheckAt)
	require.Equal(t, BalanceRecovering, r.State)
	r = OnStrongSignal(r, &s, "again", t0.Add(time.Hour))
	require.Equal(t, BalanceExhausted, r.State)
	require.Equal(t, 2, r.Level)
	require.Equal(t, t0.Add(time.Hour+300*time.Second), r.NextCheckAt)
}

func TestWeakSignalSuspect(t *testing.T) {
	s := noJitter()
	r := OnWeakSignal(BalanceRecord{State: BalanceOK}, &s, "429 insufficient balance", t0)
	require.Equal(t, BalanceSuspect, r.State)
	require.Equal(t, TierSoft, TempUnschedTier(r.TempUnschedReason()))
	require.Equal(t, t0.Add(90*time.Second), r.ExclusionUntil(&s))
	require.True(t, r.Due(t0))

	// Probe positive -> OK; negative -> EXHAUSTED.
	require.Equal(t, BalanceOK, OnProbe(r, &s, ProbePositive, "usage", "", t0).State)
	require.Equal(t, BalanceExhausted, OnProbe(r, &s, ProbeNegative, "usage", "", t0).State)
	// TTL expiry -> OK on the next success.
	ok, changed := OnSuccess(r, &s, t0.Add(91*time.Second))
	require.True(t, changed)
	require.Equal(t, BalanceOK, ok.State)

	// A weak signal never downgrades EXHAUSTED.
	ex := OnStrongSignal(BalanceRecord{}, &s, "e", t0)
	require.Equal(t, BalanceExhausted, OnWeakSignal(ex, &s, "w", t0).State)
}

func TestProbeUnknownKeepsState(t *testing.T) {
	s := noJitter()
	r := OnStrongSignal(BalanceRecord{}, &s, "e", t0)
	r2 := OnProbe(r, &s, ProbeUnknown, "usage", "timeout", t0.Add(time.Minute))
	require.Equal(t, BalanceExhausted, r2.State)
	require.Equal(t, r.Level, r2.Level)
	require.Equal(t, t0.Add(2*time.Minute), r2.NextCheckAt)
}

func TestTempUnschedTier(t *testing.T) {
	require.Equal(t, TierSoft, TempUnschedTier("transient:TRANSIENT 502"))
	require.Equal(t, TierSoft, TempUnschedTier("auth:403_cooldown (1/3): forbidden"))
	require.Equal(t, TierSoft, TempUnschedTier("OpenAI 403 temporary cooldown (1/3): x"))
	require.Equal(t, TierHard, TempUnschedTier("balance:exhausted: x"))
	require.Equal(t, TierHard, TempUnschedTier("OAuth 401: expired"))
	require.Equal(t, TierHard, TempUnschedTier("cn_balance_low: x"))
	require.Equal(t, TierHard, TempUnschedTier(""))
}

func TestTruncateRuneSafe(t *testing.T) {
	s := strings.Repeat("余", 10)
	got := truncate(s, 7)
	require.True(t, len(got) <= 7)
	require.Equal(t, "余余", got)
}
