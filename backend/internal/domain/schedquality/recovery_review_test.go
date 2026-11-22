//go:build unit

package schedquality

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Relaxation clears a line's recurrence level but its recent confirmed failure
// still governs the quiet clock while any sibling retains recurrence history.
func TestRecoveryQuietClockIncludesRelaxedLine(t *testing.T) {
	rows := recoveryGroup()
	rows[0].LastRear = machineNow.Add(-10 * time.Minute)
	rows[1].Level, rows[1].Admissions = 2, 3
	rows[1].LastRear = machineNow.Add(-30 * time.Hour)
	deadline := rows[0].LastRear.Add(LevelClearQuiet)
	require.Equal(t, deadline, QuietDeadline(rows))
	normal, actions := StepGroup(rows, map[Dimension]Evaluation{DimensionTTFO: {Verdict: VerdictPass}}, machineNow)
	require.Empty(t, actions[1])
	require.Equal(t, rows[1], normal[1])
	failed, actions := StepGroup(normal, map[Dimension]Evaluation{DimensionTTFO: {Verdict: VerdictFail}}, machineNow.Add(RegularInterval))
	require.Equal(t, ActionEnter, actions[0])
	require.Equal(t, 3, failed[0].Level)
	require.True(t, QuietDeadline(failed).IsZero(), "a restricted group cannot clear")
	early, actions := StepGroup(normal, nil, deadline.Add(-time.Nanosecond))
	require.Equal(t, 2, early[1].Level)
	require.Empty(t, actions[1])
	cleared, actions := StepGroup(early, nil, deadline)
	require.Equal(t, ActionClear, actions[1])
	require.Zero(t, cleared[1].Admissions)
	require.Zero(t, cleared[1].Level)
	require.Equal(t, rows[0].LastRear, cleared[0].LastRear)
	require.True(t, QuietDeadline(cleared).IsZero(), "old timestamps alone cannot resurrect a quiet clock")
	again, actions := StepGroup(cleared, nil, deadline.Add(RegularInterval))
	require.Equal(t, cleared, again)
	require.Equal(t, []Action{"", "", ""}, actions)
}

// Late direct calls retain the promised timeout without moving the evaluation
// watermark backwards. The service still schedules the original timestamps.
func TestRecoveryLateTimeoutUsesPromisedDeadline(t *testing.T) {
	for _, duration := range []time.Duration{time.Hour, TrialDuration} {
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
	require.Equal(t, machineNow.Add(360*time.Minute), next.Epoch)
	require.Equal(t, int64(7200), next.Basis.TrialDurationSeconds)
}
