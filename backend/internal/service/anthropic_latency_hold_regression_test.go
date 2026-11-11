//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropicRepeatedLatencyHoldDoesNotEscalate(t *testing.T) {
	now := time.Unix(2000, 0)
	svc := &GatewayService{}
	for round := 0; round < 2; round++ {
		for n := 0; n < 6; n++ {
			svc.recordAnthropicAccountLatencySample(&Account{ID: 1, Platform: PlatformAnthropic}, "claude-test", 11*time.Second, now)
		}
		hold := svc.anthropicLatencyHold.Load().snapshot(1, "claude-test", now)
		require.Equal(t, now.Add(3*time.Minute), hold.HoldUntil)
		now = hold.HoldUntil
	}
}
