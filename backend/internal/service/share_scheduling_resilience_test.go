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

func TestRequestStateAvoided(t *testing.T) {
	st := resilience.NewRequestState()
	require.False(t, st.Avoided(7))
	st.SetAvoided(map[int64]struct{}{7: {}})
	require.True(t, st.Avoided(7))
	require.False(t, st.Avoided(8))
}

func TestRechargedAccountWaitsForStabilityBeforeTierReturn(t *testing.T) {
	// A higher-tier account that ran out of balance is an account-level
	// failure: once recharged, tier return waits tiers.stable_seconds like
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
