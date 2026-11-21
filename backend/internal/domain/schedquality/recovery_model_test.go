//go:build unit

package schedquality

import (
	"compress/gzip"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

// Acceptance 2/3/4: frozen recovery_v1.py outputs, with only position/clock/history
// compared. The specification intentionally changes the post-release label.
func TestRecoveryModelSequences(t *testing.T) {
	type state struct {
		Line                Dimension
		Position            string
		Level, Admissions   int
		Epoch, Until, Entry *int
		TrialUntil          *int `json:"trial_until"`
		LastRear            *int `json:"last_rear"`
		EvaluatedAt         *int `json:"evaluated_at"`
		PendingAt           *int `json:"pending_at"`
	}
	var cases []struct {
		Name    string
		Initial []state
		Steps   []struct {
			At      int
			Verdict Verdict
			States  []state
		}
	}
	f, err := os.Open("testdata/recovery_v1.json.gz")
	require.NoError(t, err)
	defer f.Close()
	z, err := gzip.NewReader(f)
	require.NoError(t, err)
	defer z.Close()
	require.NoError(t, json.NewDecoder(z).Decode(&cases))
	stamp := func(n *int) time.Time {
		if n == nil {
			return time.Time{}
		}
		return machineNow.Add(time.Duration(*n) * time.Second)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var rows []LineState
			for _, s := range c.Initial {
				r := machineState(s.Line)
				r.Level = s.Level
				r.Admissions = s.Admissions
				r.Epoch = stamp(s.Epoch)
				r.Entry = stamp(s.Entry)
				r.Until = stamp(s.Until)
				r.TrialUntil = stamp(s.TrialUntil)
				r.LastRear = stamp(s.LastRear)
				r.EvaluatedAt = stamp(s.EvaluatedAt)
				r.Trial = s.Position == "trial"
				if s.Position == "rear" {
					r.Identity = IdentityRear
				}
				rows = append(rows, r)
			}
			for _, step := range c.Steps {
				at := machineNow.Add(time.Duration(step.At) * time.Second)
				inputs := make(map[Dimension]Evaluation)
				for _, r := range rows {
					if kind, due := Due(r, at, step.At%300 == 0, true); due {
						inputs[r.Line] = Evaluation{Kind: kind, Verdict: step.Verdict, Basis: Basis{OwnSampleAt60: new(true)}}
					}
				}
				rows, _ = StepGroup(rows, inputs, at)
				// Serialization is included at every step, covering restart state round-trips.
				raw, err := json.Marshal(rows)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &rows))
				for i, w := range step.States {
					n := rows[i]
					position := "normal"
					if n.Identity == IdentityRear {
						position = "rear"
					} else if n.Trial {
						position = "trial"
					}
					require.Equal(t, w.Position, position, "%s at %d", w.Line, step.At)
					require.Equal(t, w.Level, n.Level, "%s at %d", w.Line, step.At)
					require.Equal(t, w.Admissions, n.Admissions)
					require.Equal(t, stamp(w.Epoch), n.Epoch, "epoch at %d", step.At)
					require.Equal(t, stamp(w.Until), n.Until)
					require.Equal(t, stamp(w.TrialUntil), n.TrialUntil)
					require.Equal(t, stamp(w.LastRear), n.LastRear)
					require.Equal(t, stamp(w.PendingAt), n.PendingAt)
				}
			}
		})
	}
}
