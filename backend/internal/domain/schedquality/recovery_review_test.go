//go:build unit

package schedquality

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Late direct calls retain the promised timeout without moving the evaluation
// watermark backwards. The service still schedules the original timestamps.
func TestRecoveryLateTimeoutUsesPromisedDeadline(t *testing.T) {
	for _, duration := range []time.Duration{30 * time.Minute, TrialDuration} {
		t.Run(duration.String(), func(t *testing.T) {
			s := machineState(DimensionTTFO)
			s.Trial = true
			s.Epoch = machineNow
			s.TrialUntil = machineNow.Add(duration)
			at := machineNow.Add(200 * time.Minute)
			next, action := Step(s, EventTrial, VerdictUnknown, Basis{}, at)
			require.Equal(t, ActionTrialTimeout, action)
			require.Equal(t, s.TrialUntil, next.Epoch)
			require.Equal(t, int64(duration/time.Second), next.Basis.TrialDurationSeconds)
			require.Equal(t, at, next.EvaluatedAt)
			repeated, action := Step(next, EventTrial, VerdictUnknown, Basis{}, at)
			require.Empty(t, action)
			require.Equal(t, next, repeated)
		})
	}
	rear := machineState(DimensionTTFO)
	rear.Identity = IdentityRear
	rear.Until = machineNow.Add(240 * time.Minute)
	next, action := Step(rear, EventReview, VerdictUnknown, Basis{}, machineNow.Add(500*time.Minute))
	require.Equal(t, ActionTrialTimeout, action)
	require.Equal(t, machineNow.Add(300*time.Minute), next.Epoch)
	require.Equal(t, int64(3600), next.Basis.TrialDurationSeconds)
}
