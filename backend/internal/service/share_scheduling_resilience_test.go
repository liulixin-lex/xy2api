//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

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
