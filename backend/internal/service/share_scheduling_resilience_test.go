//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func TestShareStateReasonSeparatesBalanceExhaustion(t *testing.T) {
	// Spec (session-failure-handling): balance exhaustion moves the session
	// at once, also under share scheduling; soft cooldowns keep their
	// recovery time so soft spill can keep the binding.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	now := time.Now()
	until := now.Add(2 * time.Minute)
	acc := func(reason string) *Account {
		return &Account{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &until, TempUnschedulableReason: reason}
	}
	reason, recoverAt := shareAccountStateReason(acc(resilience.ReasonPrefixBalanceExhausted+": HTTP 402"), now)
	require.Equal(t, sharesched.ReasonBalanceExhausted, reason)
	require.True(t, recoverAt.IsZero())
	reason, recoverAt = shareAccountStateReason(acc(resilience.ReasonPrefixTransient+"TRANSIENT HTTP 502"), now)
	require.Equal(t, sharesched.ReasonTempUnschedulable, reason)
	require.True(t, recoverAt.Equal(until))
	reason, recoverAt = shareAccountStateReason(acc("proxy probe failed"), now)
	require.Equal(t, sharesched.ReasonTempUnschedulable, reason, "other pauses keep the 0.3.0 handling")
	require.True(t, recoverAt.Equal(until))

	off := resilience.Defaults()
	off.Enabled = false
	resilience.Publish(off)
	reason, recoverAt = shareAccountStateReason(acc(resilience.ReasonPrefixBalanceExhausted+": HTTP 402"), now)
	require.Equal(t, sharesched.ReasonTempUnschedulable, reason, "switch off: 0.3.0 handling")
	require.True(t, recoverAt.Equal(until))
}

// TestShareSoftRecoveryFollowsResiliencePenalties: only soft penalties that
// end within the hard rate-limit threshold give a recovery time (design §4).
// With the resilience master switch off the 0.3.0 rule applies: any state
// that ends within 300 seconds.
func TestShareSoftRecoveryFollowsResiliencePenalties(t *testing.T) {
	orig := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(orig) })
	off := resilience.Defaults()
	off.Enabled = false
	now := time.Now()
	soon, late := now.Add(time.Minute), now.Add(10*time.Minute)
	for _, tc := range []struct {
		name    string
		acc     Account
		on, off time.Time
	}{
		{"transient cooldown", Account{TempUnschedulableUntil: &soon, TempUnschedulableReason: resilience.ReasonPrefixTransient + "HTTP 502"}, soon, soon},
		{"empty reason", Account{TempUnschedulableUntil: &soon}, time.Time{}, soon},
		{"administrator rule", Account{TempUnschedulableUntil: &soon, TempUnschedulableReason: "keyword matched"}, time.Time{}, soon},
		{"balance pause", Account{TempUnschedulableUntil: &soon, TempUnschedulableReason: resilience.ReasonPrefixBalanceExhausted + ": HTTP 402"}, time.Time{}, soon},
		{"long administrator rule", Account{TempUnschedulableUntil: &late, TempUnschedulableReason: "keyword matched"}, time.Time{}, time.Time{}},
		{"short rate limit", Account{RateLimitResetAt: &soon}, soon, soon},
		{"long rate limit", Account{RateLimitResetAt: &late}, time.Time{}, time.Time{}},
		{"short overload", Account{OverloadUntil: &soon}, soon, soon},
		{"long overload", Account{OverloadUntil: &late}, time.Time{}, time.Time{}},
		{"disabled", Account{Status: StatusDisabled}, time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := tc.acc
			if acc.Status == "" {
				acc.Status = StatusActive
			}
			acc.Schedulable = true
			resilience.Publish(resilience.Defaults())
			_, end := shareAccountStateReason(&acc, now)
			require.True(t, tc.on.Equal(shareSoftRecovery(&acc, "", now, nil, end)), "resilience on")
			resilience.Publish(off)
			_, end = shareAccountStateReason(&acc, now)
			require.True(t, tc.off.Equal(shareSoftRecovery(&acc, "", now, nil, end)), "resilience off")
		})
	}
}

func TestRequestStateAvoided(t *testing.T) {
	st := resilience.NewRequestState()
	require.False(t, st.Avoided(7))
	st.SetAvoided(map[int64]struct{}{7: {}})
	require.True(t, st.Avoided(7))
	require.False(t, st.Avoided(8))
}

func TestRechargedAccountWaitsForStabilityBeforeTierReturn(t *testing.T) {
	// A higher-tier account that ran out of balance is an account-level
	// failure: once recharged, tier return waits sharesched.StableFor like
	// after any other health problem.
	s := NewShareSchedulingService(nil, nil, nil)
	now := time.Now()
	require.True(t, s.observeEligibility(7, true, "", now).IsZero(), "never seen failing")
	s.observeEligibility(7, false, sharesched.ReasonBalanceExhausted, now)
	require.True(t, s.observeEligibility(7, true, "", now.Add(time.Minute)).Equal(now))
	s.observeEligibility(8, false, sharesched.ReasonQuotaAutoPause, now)
	require.False(t, s.observeEligibility(8, true, "", now).IsZero())
	s.observeEligibility(9, false, sharesched.ReasonModelUnsupported, now)
	require.True(t, s.observeEligibility(9, true, "", now).IsZero(), "request-specific gates say nothing about health")
}
