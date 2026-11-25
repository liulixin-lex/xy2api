//go:build unit

package schedquality

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Persisted state must preserve deadlines and watermarks across every phase.
func TestRecoveryStateRoundTrip(t *testing.T) {
	start := machineNow
	row := machineState(DimensionTTFO)
	for _, tc := range []struct {
		kind       EventKind
		verdict    Verdict
		atOffset   int
		action     Action
		wireAction string
	}{
		{EventRegular, VerdictFail, 0, ActionEnter, "enter"},
		{EventReview, VerdictFail, 60, ActionRenew, "renew"},
		{EventReview, VerdictUnknown, 120, ActionTrial, "trial"},
		{EventTrial, VerdictFail, 135, ActionTrialFail, "trial_fail"},
		{EventReview, VerdictUnknown, 195, ActionTrial, "trial"},
		{EventTrial, VerdictPass, 210, ActionTrialPass, "trial_pass"},
		{EventRegular, VerdictFail, 215, ActionEnter, "enter"},
		{EventReview, VerdictUnknown, 275, ActionTrial, "trial"},
		{EventTrial, VerdictUnknown, 335, ActionTrialTimeout, "trial_timeout"},
	} {
		at := start.Add(time.Minute * time.Duration(tc.atOffset))
		row.Level = 4 // Every transition must discard a valid legacy level.
		next, action := machineStep(row, tc.kind, tc.verdict, at)
		require.Equal(t, tc.action, action)
		require.Zero(t, next.Level)
		raw, err := json.Marshal(next)
		require.NoError(t, err)
		var loaded LineState
		require.NoError(t, json.Unmarshal(raw, &loaded))
		require.Equal(t, next, loaded)
		decision := Decision{At: at, Key: next.LineKey, Action: action, Level: next.Level, Until: next.Until, Basis: *next.Basis}
		raw, err = json.Marshal(decision)
		require.NoError(t, err)
		// Existing readers expect numeric level, string action and RFC3339 clocks.
		var wire struct {
			Action string
			Level  int
			At     time.Time
			Until  time.Time
			Basis  Basis
		}
		require.NoError(t, json.Unmarshal(raw, &wire))
		require.Equal(t, tc.wireAction, wire.Action)
		require.Zero(t, wire.Level)
		require.Equal(t, at, wire.At)
		require.Equal(t, next.Until, wire.Until)
		repeated, action := machineStep(loaded, tc.kind, tc.verdict, at)
		require.Empty(t, action)
		require.Equal(t, loaded, repeated)
		row = loaded
	}
}
