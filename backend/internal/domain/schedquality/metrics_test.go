//go:build unit

package schedquality

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSummaryAcrossModelsAndRequestCounts(t *testing.T) {
	rows := machineSamples(120)
	// All models and groups for this account contribute to the same display window.
	for i := range rows {
		if i%2 == 0 {
			rows[i].Model = "another-model"
			rows[i].GroupID = 20
		}
	}
	for i, ms := range []int{101, 51, 200} {
		rows[i].TTFOMs = machineInt(ms)
	}
	rows = append(rows, rows[0]) // Duplicate completion for the same attempt.
	failSamples(rows[5:], 1)
	rows[6].Outcome = OutcomeOther
	rows[6].Class = ClassUnknown
	rows[7].Outcome = OutcomeOther
	rows[7].Class = ClassRequest
	retry := rows[5]
	retry.AttemptID = "successful-retry"
	retry.StartedAt = rows[5].FinishedAt
	retry.FinishedAt = retry.StartedAt.Add(time.Millisecond)
	retry.Outcome = OutcomeOK
	retry.Class = ClassNone
	rows = append(rows, retry)
	other := rows[0]
	other.AccountID = 2
	other.AttemptID = "other-account"
	rows = append(rows, other)
	out := SummarizeAccount(rows, 1, time.Time{}, machineNow)
	if out.ErrorN != 118 || out.ErrorBad != 0 {
		t.Fatalf("request counts, cap or retry: %+v", out)
	}
	require.Equal(t, FirstOutputSummary{N: 3, P50Ms: machineInt(101)}, out.FirstOutput)
	if out.CacheTurns != 118 || out.CacheRatio == nil || *out.CacheRatio != .8 {
		t.Fatalf("cache totals %+v", out)
	}
}

func TestSummaryContinuationCacheAndReset(t *testing.T) {
	rows := machineSamples(2)
	rows[0].InputTokens = machineInt(10)
	rows[0].CacheReadTokens = machineInt(90)
	retry := rows[0]
	retry.AttemptID = "retry"
	retry.StartedAt = rows[0].FinishedAt
	retry.FinishedAt = retry.StartedAt.Add(time.Millisecond)
	retry.InputTokens = machineInt(90)
	retry.CacheReadTokens = machineInt(10)
	rows[1].Continuation = false
	rows[1].InputTokens = machineInt(100)
	rows[1].CacheReadTokens = machineInt(0)
	rows = append(rows, retry)
	out := SummarizeAccount(rows, 1, time.Time{}, machineNow)
	if out.CacheTurns != 2 || out.CacheRatio == nil || *out.CacheRatio != .5 {
		t.Fatalf("continuation cache: %+v", out)
	}
	// Continuations older than 30m remain in the one-hour display window.
	for i := range rows {
		rows[i].StartedAt = rows[i].StartedAt.Add(-40 * time.Minute)
		rows[i].FinishedAt = rows[i].FinishedAt.Add(-40 * time.Minute)
	}
	out = SummarizeAccount(rows, 1, time.Time{}, machineNow)
	if out.CacheTurns != 2 || AssessCache(rows, .8, time.Time{}, machineNow).Turns != 0 {
		t.Fatal("summary accidentally uses line window")
	}
	reset := rows[1].StartedAt
	out = SummarizeAccount(rows, 1, reset, machineNow)
	if !out.From.Equal(reset) || out.ErrorN != 0 || out.CacheRatio != nil || out.FirstOutput.P50Ms != nil {
		t.Fatalf("reset boundary %+v", out)
	}
	out = SummarizeAccount(nil, 1, time.Time{}, machineNow)
	if out.ErrorN != 0 || out.CacheRatio != nil || out.FirstOutput != (FirstOutputSummary{}) || out.Slow10s != (Slow10sSummary{}) {
		t.Fatal("unknown metrics must stay absent")
	}
}

func TestAssessmentJSONContract(t *testing.T) {
	for _, tc := range []struct {
		value any
		keys  []string
	}{
		{ErrorAssessment{AttemptIDs: []string{"private"}, SourceGroups: []int64{10}}, []string{"verdict", "limit", "clear", "failed", "unknown", "padded", "lower_fail", "lower_pass"}},
		{CacheAssessment{AttemptIDs: []string{"private"}, SourceGroups: []int64{10}}, []string{"verdict", "min_ratio", "ratio", "turns", "sessions", "low_sessions", "low_sessions_10", "read_tokens", "input_tokens"}},
	} {
		data, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err = json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		if len(object) != len(tc.keys) {
			t.Fatalf("JSON fields %s", data)
		}
		for _, key := range tc.keys {
			if _, ok := object[key]; !ok {
				t.Fatalf("missing %s: %s", key, data)
			}
		}
	}
}

func TestDomainFunctionsPreserveInputs(t *testing.T) {
	rows := machineSamples(100)
	failSamples(rows, 50)
	before, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	_ = AssessError(rows, nil, .05, time.Time{}, machineNow)
	_ = AssessCache(rows, .8, time.Time{}, machineNow)
	_ = SummarizeAccount(rows, 1, time.Time{}, machineNow)
	after, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("assessment changed caller-owned samples")
	}
}

func TestSummaryFirstOutputMedian(t *testing.T) {
	for _, tc := range []struct {
		name              string
		outputs, timeouts int
		want              *int
	}{
		{"only outputs", 80, 0, machineInt(40)},
		{"one fifth timeouts", 80, 20, machineInt(50)},
		{"half timeouts", 2, 2, machineInt(2)},
		{"majority timeouts", 2, 3, nil},
		{"only timeouts", 0, 3, nil},
		{"empty", 0, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(tc.outputs + tc.timeouts)
			for i := range rows {
				if i < tc.outputs {
					rows[i].TTFOMs = machineInt(tc.outputs - i)
				} else {
					rows[i].Outcome = OutcomeTimeout
				}
			}
			out := SummarizeAccount(rows, 1, time.Time{}, machineNow)
			require.Equal(t, FirstOutputSummary{N: len(rows), Timeouts: tc.timeouts, P50Ms: tc.want}, out.FirstOutput)
		})
	}
}

func TestSummaryStreamingAttemptClassification(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stream       bool
		outcome      Outcome
		output, wait *int
		first        FirstOutputSummary
		slow         Slow10sSummary
	}{
		{"error before output", true, OutcomeAccountFault, nil, nil, FirstOutputSummary{}, Slow10sSummary{}},
		{"canceled before output", true, OutcomeCanceled, nil, nil, FirstOutputSummary{}, Slow10sSummary{}},
		{"success without output", true, OutcomeOK, nil, nil, FirstOutputSummary{}, Slow10sSummary{}},
		{"error after output at line", true, OutcomeAccountFault, machineInt(10000), nil, FirstOutputSummary{N: 1, P50Ms: machineInt(10000)}, Slow10sSummary{N: 1}},
		{"canceled after slow output", true, OutcomeCanceled, machineInt(10001), nil, FirstOutputSummary{N: 1, P50Ms: machineInt(10001)}, Slow10sSummary{N: 1, Slow: 1}},
		{"timeout after immediate output", true, OutcomeTimeout, machineInt(0), machineInt(15000), FirstOutputSummary{N: 1, P50Ms: machineInt(0)}, Slow10sSummary{N: 1}},
		{"timeout at line", true, OutcomeTimeout, machineInt(-1), machineInt(10000), FirstOutputSummary{N: 1, Timeouts: 1}, Slow10sSummary{N: 1, Slow: 1, Timeouts: 1}},
		{"timeout above line", true, OutcomeTimeout, nil, machineInt(10001), FirstOutputSummary{N: 1, Timeouts: 1}, Slow10sSummary{N: 1, Slow: 1, Timeouts: 1}},
		{"timeout below line", true, OutcomeTimeout, nil, machineInt(9999), FirstOutputSummary{N: 1, Timeouts: 1}, Slow10sSummary{}},
		{"timeout without wait", true, OutcomeTimeout, nil, nil, FirstOutputSummary{N: 1, Timeouts: 1}, Slow10sSummary{}},
		{"nonstream output", false, OutcomeOK, machineInt(10001), nil, FirstOutputSummary{}, Slow10sSummary{}},
		{"nonstream timeout", false, OutcomeTimeout, nil, machineInt(10000), FirstOutputSummary{}, Slow10sSummary{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(1)
			s := &rows[0]
			s.Stream, s.Outcome, s.TTFOMs, s.CensoredMs = tc.stream, tc.outcome, tc.output, tc.wait
			s.ClientCanceled = s.Outcome == OutcomeCanceled
			out := SummarizeAccount(rows, 1, time.Time{}, machineNow)
			require.Equal(t, tc.first, out.FirstOutput)
			require.Equal(t, tc.slow, out.Slow10s)
		})
	}
}
