//go:build unit

package schedquality

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestSummaryPairedLatenciesAndRequestCounts(t *testing.T) {
	rows := machineSamples(120)
	// All models contribute; only paired streaming times enter all three medians.
	for i := range rows {
		if i%2 == 0 {
			rows[i].Model = "another-model"
		}
	}
	for i, pair := range [][2]int{{1, 101}, {50, 51}, {60, 200}} {
		rows[i].FirstEventMs = machineInt(pair[0])
		rows[i].TTFOMs = machineInt(pair[1])
	}
	rows[3].FirstEventMs = machineInt(999) // missing matching output
	rows[4].Stream = false
	rows[4].FirstEventMs = machineInt(0)
	rows[4].TTFOMs = machineInt(0)
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
	if out.FirstEventP50Ms == nil || *out.FirstEventP50Ms != 50 || *out.FirstOutputP50Ms != 101 || *out.GapP50Ms != 100 {
		t.Fatalf("paired median or difference of medians: %+v", out)
	}
	if out.FirstOutputN != 3 {
		t.Fatalf("paired streaming sample count = %d, want 3", out.FirstOutputN)
	}
	if out.CacheAllRequests != 118 || out.CacheTurns != 118 || out.CacheRatio == nil || *out.CacheRatio != .8 {
		t.Fatalf("cache totals %+v", out)
	}
}

func TestSummaryCacheDenominatorsAndReset(t *testing.T) {
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
	if out.CacheTurns != 2 || out.CacheAllRequests != 2 || *out.CacheRatio != .5 || math.Abs(*out.CacheAllRatio-.05) > 1e-12 {
		t.Fatalf("attempt vs request cache: %+v", out)
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
	if !out.From.Equal(reset) || out.ErrorN != 0 || out.CacheRatio != nil || out.FirstOutputP50Ms != nil {
		t.Fatalf("reset boundary %+v", out)
	}
	out = SummarizeAccount(nil, 1, time.Time{}, machineNow)
	if out.ErrorN != 0 || out.CacheAllRatio != nil || out.GapP50Ms != nil || out.FirstOutputN != 0 {
		t.Fatal("unknown metrics must stay absent")
	}
}

func TestAssessmentJSONContract(t *testing.T) {
	for _, tc := range []struct {
		value any
		keys  []string
	}{
		{ErrorAssessment{AttemptIDs: []string{"private"}, SourceGroups: []int64{10}}, []string{"verdict", "limit", "clear", "failed", "unknown", "padded", "lower_fail", "lower_pass"}},
		{CacheAssessment{AttemptIDs: []string{"private"}, SourceGroups: []int64{10}}, []string{"verdict", "min_ratio", "ratio", "turns", "sessions", "low_sessions", "read_tokens", "input_tokens"}},
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

func TestSummaryGapClampsEachPairedSample(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pairs [][2]int
		want  int
	}{
		{"mixed", [][2]int{{200, 100}, {10, 40}, {0, 50}}, 30},
		{"mostly earlier output", [][2]int{{200, 100}, {300, 100}, {0, 50}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(5)
			for i, pair := range tc.pairs {
				rows[i].FirstEventMs = machineInt(pair[0])
				rows[i].TTFOMs = machineInt(pair[1])
			}
			rows[3].FirstEventMs = nil
			rows[3].TTFOMs = machineInt(9999)
			rows[4].FirstEventMs = machineInt(9999)
			rows[4].TTFOMs = nil
			out := SummarizeAccount(rows, 1, time.Time{}, machineNow)
			if out.GapP50Ms == nil || *out.GapP50Ms != tc.want || out.FirstOutputN != len(tc.pairs) {
				t.Fatalf("gap=%v count=%d want=%d/%d", out.GapP50Ms, out.FirstOutputN, tc.want, len(tc.pairs))
			}
			out = SummarizeAccount(rows[3:], 1, time.Time{}, machineNow)
			if out.GapP50Ms != nil || out.FirstOutputN != 0 {
				t.Fatalf("unpaired times entered median: %+v", out)
			}
		})
	}
}
