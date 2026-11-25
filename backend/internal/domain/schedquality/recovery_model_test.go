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
		kind     EventKind
		verdict  Verdict
		atOffset int
		action   Action
	}{
		{EventRegular, VerdictFail, 0, ActionEnter},
		{EventReview, VerdictUnknown, 60, ActionTrial},
		{EventTrial, VerdictUnknown, 120, ActionTrialTimeout},
		{EventRegular, VerdictFail, 125, ActionEnter},
	} {
		at := start.Add(time.Minute * time.Duration(tc.atOffset))
		next, action := machineStep(row, tc.kind, tc.verdict, at)
		require.Equal(t, tc.action, action)
		raw, err := json.Marshal(next)
		require.NoError(t, err)
		var loaded LineState
		require.NoError(t, json.Unmarshal(raw, &loaded))
		require.Equal(t, next, loaded)
		repeated, action := machineStep(loaded, tc.kind, tc.verdict, at)
		require.Empty(t, action)
		require.Equal(t, loaded, repeated)
		row = loaded
	}
}
