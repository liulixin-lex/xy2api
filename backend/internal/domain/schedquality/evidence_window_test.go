//go:build unit

package schedquality

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvidenceWindowsMatchAdmissionBoundaries(t *testing.T) {
	for _, line := range Lines {
		t.Run(string(line), func(t *testing.T) {
			window := map[Dimension]time.Duration{DimensionTTFO: RuleBWindow, DimensionError: ErrorWindow, DimensionCache: CacheWindow}[line]
			from := machineNow.Add(-window)
			for _, tc := range []struct {
				name string
				at   time.Time
				want int
			}{
				{"before_start", from.Add(-time.Nanosecond), 0},
				{"at_start", from, 1},
				{"at_end", machineNow, 1},
				{"after_end", machineNow.Add(time.Nanosecond), 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rows := machineSamples(1)
					rows[0].StartedAt, rows[0].FinishedAt, rows[0].InsertedAt = tc.at.Add(-time.Second), tc.at, tc.at
					var gotFrom, gotTo time.Time
					var n int
					switch line {
					case DimensionTTFO:
						in := RuleBInput{Now: machineNow, LimitSeconds: 1, Scope: RuleBScope{AccountID: 1, GroupID: 10, Model: "synthetic-model"}, Samples: []RuleBSample{{AccountID: 1, GroupID: 10, Model: "synthetic-model", SessionKey: "s", StartedAt: rows[0].StartedAt, AvailableAt: tc.at, Stream: true, TTFOMs: 2000, Outcome: OutcomeOK}}}
						r, err := EvaluateRuleB(in, DefaultRuleBParams())
						require.NoError(t, err)
						e := NewTTFTEvidence(r, in.LimitSeconds)
						gotFrom, gotTo, n = e.WindowFrom, e.WindowTo, r.SamplesTotal
					case DimensionError:
						e := AssessError(rows, nil, .05, time.Time{}, machineNow)
						gotFrom, gotTo, n = e.WindowFrom, e.WindowTo, e.Clear
					case DimensionCache:
						e := AssessCache(rows, .8, time.Time{}, machineNow)
						gotFrom, gotTo, n = e.WindowFrom, e.WindowTo, e.Turns
					}
					require.Equal(t, from, gotFrom)
					require.Equal(t, machineNow, gotTo)
					require.Equal(t, tc.want, n)
				})
			}
		})
	}
}

func TestEvidenceWindowsAbsentInLegacyJSON(t *testing.T) {
	var basis Basis
	require.NoError(t, json.Unmarshal([]byte(`{"ttft":{"n":24},"error":{"clear":50},"cache":{"turns":20}}`), &basis))
	require.True(t, basis.TTFT.WindowFrom.IsZero())
	require.True(t, basis.TTFT.WindowTo.IsZero())
	require.True(t, basis.Error.WindowFrom.IsZero())
	require.True(t, basis.Error.WindowTo.IsZero())
	require.True(t, basis.Cache.WindowFrom.IsZero())
	require.True(t, basis.Cache.WindowTo.IsZero())
	raw, err := json.Marshal(basis)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "window_from")
	require.NotContains(t, string(raw), "window_to")
}
