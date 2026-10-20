//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// TestShareSoftFailureAttempt: only attempts that gateway resilience rates a
// soft cooldown count against an account's health (design §11.1).
func TestShareSoftFailureAttempt(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   ShareSchedulingUpstreamAttempt
		want bool
	}{
		{"transient 5xx", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 502, Category: "TRANSIENT"}, true},
		{"first output timeout", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "first_output_timeout", StatusCode: 504, Category: "FIRST_OUTPUT_TIMEOUT"}, true},
		{"stall", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "stream_error", Category: "STALL"}, true},
		{"transport error", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "request_error", Message: "dial tcp: i/o timeout"}, true},
		{"client cancel", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "request_error", Message: "context canceled"}, false},
		{"client cancel category", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "request_error", Category: "CLIENT_CANCEL"}, false},
		{"recovered retry", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "retry", StatusCode: 503, Category: "TRANSIENT"}, true},
		{"balance", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 402, Category: "BALANCE"}, false},
		{"auth", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 401, Category: "AUTH"}, false},
		{"rate limit", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 429, Category: "RATE_LIMIT"}, false},
		{"rate limit reset", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 429, Category: "RATE_LIMIT_RESET"}, false},
		{"capacity shed", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 529, Category: "CAPACITY_SHED"}, false},
		{"request error", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "http_error", StatusCode: 400, Category: "CLIENT_ERROR"}, false},
		{"model unsupported", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 404, Category: "MODEL_UNSUPPORTED"}, false},
		{"unclassified answer", ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: "failover", StatusCode: 500}, false},
		{"no account", ShareSchedulingUpstreamAttempt{Kind: "failover", StatusCode: 502, Category: "TRANSIENT"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shareSoftFailureAttempt(tc.ev))
		})
	}
}

func shareHealthOf(t *testing.T, members []shareHealthMember, i int) *float64 {
	t.Helper()
	return shareTierHealth(members)[i].Factor
}

// TestShareTierHealthFactors: bounded down-weighting relative to the best
// healthy member of the tier (design §11.1).
func TestShareTierHealthFactors(t *testing.T) {
	healthy := func(attempts, failures int64) shareHealthMember {
		return shareHealthMember{Healthy: true, Stats: shareHealthStats{Attempts: attempts, SoftFailures: failures}}
	}
	// The production replay of the readiness review: 1.1%, 9.0%, 20.8%.
	replay := []shareHealthMember{healthy(4318, 48), healthy(2070, 187), healthy(900, 187)}
	got := shareTierHealth(replay)
	require.InDelta(t, 1, *got[0].Factor, 1e-9)
	require.InDelta(t, 0.5, *got[1].Factor, 1e-9, "about 4.8 points beyond the dead zone halves the weight")
	require.InDelta(t, 0.2, *got[2].Factor, 1e-9, "the floor keeps observation traffic")
	require.InDelta(t, 48.0/4318, got[2].Baseline, 1e-9)
	require.InDelta(t, 187.0/900, got[2].Rate, 1e-9)

	for _, tc := range []struct {
		name    string
		members []shareHealthMember
		i       int
		want    float64
	}{
		{"dead zone", []shareHealthMember{healthy(1000, 10), healthy(1000, 35)}, 1, 1},
		{"one point beyond", []shareHealthMember{healthy(10000, 0), healthy(10000, 400)}, 1, 0.85},
		{"small sample shrinks", []shareHealthMember{healthy(1000, 0), healthy(3, 3)}, 1, 0.25},
		{"all equally bad", []shareHealthMember{healthy(1000, 300), healthy(1000, 300)}, 1, 1},
		// The baseline comes from members with at least 20 attempts: two
		// lucky attempts must not make a normal account look bad.
		{"low sample peer ignored for baseline", []shareHealthMember{healthy(2, 0), healthy(1000, 50), healthy(1000, 80)}, 2, 1},
		{"baseline falls back to any sample", []shareHealthMember{healthy(5, 0), healthy(10, 5)}, 1, 0.2},
		{"only healthy member", []shareHealthMember{healthy(1000, 500), {Healthy: false, Stats: shareHealthStats{Attempts: 1000}}}, 0, 1},
		{"unhealthy members rate against the healthy baseline", []shareHealthMember{healthy(1000, 0), healthy(1000, 0), {Stats: shareHealthStats{Attempts: 1000, SoftFailures: 500}}}, 2, 0.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.want, *shareHealthOf(t, tc.members, tc.i), 1e-9)
		})
	}

	got = shareTierHealth([]shareHealthMember{healthy(2, 0), healthy(1000, 50), healthy(1000, 80)})
	require.InDelta(t, 0.05, got[2].Baseline, 1e-9, "the baseline is the best member with at least 20 attempts")

	got = shareTierHealth([]shareHealthMember{healthy(1000, 0), healthy(0, 0)})
	require.True(t, got[1].Insufficient, "no attempt in the window: no data")
	require.Nil(t, got[1].Factor)
	require.False(t, got[0].Insufficient)
}

func TestShareHealthFactorQuantizes(t *testing.T) {
	for excess, want := range map[float64]float64{0: 1, 0.01: 0.85, 0.025: 0.7, 0.05: 0.5, 0.1: 0.25, 0.5: 0.2} {
		require.InDelta(t, want, shareHealthFactorOf(0.01+0.03+excess, 0.01), 1e-9, "excess %v", excess)
	}
}

// TestShareRampAfterLongOutages: weights climb from 0.1 to 1 within ten
// minutes after a balance pause, a hard penalty or a long observed outage;
// a short soft cooldown does not ramp (design §11.2).
func TestShareRampAfterLongOutages(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	for _, tc := range []struct {
		name     string
		acc      Account
		observed time.Time
		cause    string
		factor   float64
	}{
		{"balance pause ended", Account{TempUnschedulableUntil: at(-3 * time.Minute), TempUnschedulableReason: resilience.ReasonPrefixBalanceExhausted + ": HTTP 402"},
			time.Time{}, ShareRampBalancePause, 0.35},
		{"hard pause ended", Account{TempUnschedulableUntil: at(-time.Minute), TempUnschedulableReason: "keyword matched"},
			time.Time{}, ShareRampHardPenalty, 0.2},
		{"long rate limit ended", Account{RateLimitedAt: at(-20 * time.Minute), RateLimitResetAt: at(-5 * time.Minute)},
			time.Time{}, ShareRampHardPenalty, 0.55},
		{"short rate limit ended", Account{RateLimitedAt: at(-3 * time.Minute), RateLimitResetAt: at(-time.Minute)}, time.Time{}, "", 1},
		{"soft cooldown ended", Account{TempUnschedulableUntil: at(-time.Minute), TempUnschedulableReason: resilience.ReasonPrefixTransient + "HTTP 502"},
			time.Time{}, "", 1},
		{"ramp over", Account{TempUnschedulableUntil: at(-11 * time.Minute), TempUnschedulableReason: "keyword matched"}, time.Time{}, "", 1},
		{"pause still on", Account{TempUnschedulableUntil: at(time.Minute), TempUnschedulableReason: "keyword matched"}, time.Time{}, "", 1},
		{"observed re-enable", Account{}, now.Add(-90 * time.Second), ShareRampReenabled, 0.25},
		{"latest recovery wins", Account{TempUnschedulableUntil: at(-8 * time.Minute), TempUnschedulableReason: "keyword matched"},
			now.Add(-time.Minute), ShareRampReenabled, 0.2},
		{"no outage", Account{}, time.Time{}, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := tc.acc
			acc.Status, acc.Schedulable = StatusActive, true
			ramp := shareRampOf(&acc, tc.observed, now)
			require.InDelta(t, tc.factor, ramp.Factor, 1e-9)
			require.Equal(t, tc.cause, ramp.Cause)
			if tc.cause != "" {
				require.True(t, ramp.FullAt.Equal(ramp.Since.Add(shareRampDuration)))
			}
		})
	}
}

// TestShareWeightCombinesFactors: the effective weight is the load factor
// times both factors, at least 5% of the load factor.
func TestShareWeightCombinesFactors(t *testing.T) {
	lf := 100
	acc := &Account{LoadFactor: &lf, Status: StatusActive, Schedulable: true}
	half := 0.5
	w := shareCombineWeight(acc, &half, shareRamp{Factor: 0.4})
	require.Equal(t, 100, w.LoadFactor)
	require.InDelta(t, 20, w.Effective, 1e-9)
	floor := 0.2
	w = shareCombineWeight(acc, &floor, shareRamp{Factor: 0.1})
	require.InDelta(t, 5, w.Effective, 1e-9, "never below 5% of the load factor")
	w = shareCombineWeight(acc, nil, shareRamp{Factor: 1})
	require.InDelta(t, 100, w.Effective, 1e-9, "no data: no down-weighting")
}
