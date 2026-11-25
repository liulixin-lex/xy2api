//go:build unit

package schedquality

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

var machineNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func machineInt(v int) *int { return &v }
func machineSamples(n int) []AttemptSample {
	rows := make([]AttemptSample, n)
	for i := range rows {
		finish := machineNow.Add(-time.Duration(n-i) * time.Second)
		rows[i] = AttemptSample{AttemptID: fmt.Sprintf("attempt-%03d", i), RequestID: fmt.Sprintf("request-%03d", i),
			AccountID: 1, GroupID: 10, Model: "synthetic-model", SessionKey: fmt.Sprintf("session-%d", i%2),
			StartedAt: finish.Add(-time.Second), FinishedAt: finish, InsertedAt: finish,
			Stream: true, Outcome: OutcomeOK, Continuation: true,
			InputTokens: machineInt(20), CacheReadTokens: machineInt(80), CacheCreationTokens: machineInt(0)}
	}
	return rows
}
func failSamples(rows []AttemptSample, n int) {
	for i := 0; i < n; i++ {
		rows[i].Outcome = OutcomeAccountFault
		rows[i].Class = ClassUpstream
	}
}

func TestAssessErrorWilsonBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		clear, failed, unknown int
		want                   Verdict
	}{
		{"five-of-fifty", 50, 5, 0, VerdictPass},
		{"five-padded-to-fifty", 5, 5, 0, VerdictUnknown},
		{"six-of-fifty", 50, 6, 0, VerdictFail},
		{"six-padded-to-fifty", 6, 6, 0, VerdictFail},
		{"forty-nine-clear", 49, 0, 0, VerdictUnknown},
		{"fifty-clear", 50, 0, 0, VerdictPass},
		{"unknown-treated-as-bad-pass", 50, 0, 1, VerdictPass},
		{"unknown-treated-as-bad-blocks-pass", 50, 5, 1, VerdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(tc.clear + tc.unknown)
			failSamples(rows, tc.failed)
			for i := tc.clear; i < len(rows); i++ {
				rows[i].Outcome = OutcomeOther
				rows[i].Class = ClassUnknown
			}
			got := AssessError(rows, nil, .05, time.Time{}, machineNow)
			if got.Verdict != tc.want || got.Clear != tc.clear || got.Failed != tc.failed || got.Unknown != tc.unknown ||
				got.Padded != max(50, len(rows)) || got.Limit != .05 {
				t.Fatalf("assessment: %+v", got)
			}
			if math.Abs(got.LowerPass-WilsonLower(tc.failed+tc.unknown, got.Padded)) > 1e-15 {
				t.Fatal("unknown not charged as failure")
			}
		})
	}
}

func TestAssessErrorRecoveryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		clear, failed, unknown int
		limit                  float64
		regular, recovery      Verdict
	}{
		{"above-floor", 52, 5, 0, .05, VerdictPass, VerdictUnknown},
		{"equal-to-floor", 100, 5, 0, .05, VerdictPass, VerdictPass},
		{"equal-to-group-limit", 100, 7, 0, .07, VerdictPass, VerdictPass},
		{"equal-to-rounded-group-limit", 100, 29, 0, .29, VerdictPass, VerdictPass},
		{"below-group-limit", 52, 5, 0, .10, VerdictPass, VerdictPass},
		{"above-group-limit", 52, 5, 0, .08, VerdictPass, VerdictUnknown},
		{"unknown-excluded-from-rate", 97, 5, 3, .05, VerdictPass, VerdictUnknown},
		{"already-failed", 52, 6, 0, .05, VerdictFail, VerdictFail},
		{"already-unknown", 49, 5, 0, .05, VerdictUnknown, VerdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(tc.clear + tc.unknown)
			failSamples(rows, tc.failed)
			for i := tc.clear; i < len(rows); i++ {
				rows[i].Outcome = OutcomeOther
				rows[i].Class = ClassUnknown
			}
			regular := AssessError(rows, nil, tc.limit, time.Time{}, machineNow)
			if regular.Verdict != tc.regular || regular.RecoveryOverLimit {
				t.Fatalf("regular assessment: %+v", regular)
			}
			want := regular
			want.Verdict = tc.recovery
			want.RecoveryOverLimit = tc.regular != tc.recovery
			if got := regular.ForRecovery(); !reflect.DeepEqual(got, want) {
				t.Fatalf("recovery assessment: %+v, want %+v", got, want)
			}
		})
	}
}

func TestAssessErrorRequestFinalResult(t *testing.T) {
	rows := machineSamples(50)
	retry := rows[0]
	failSamples(rows, 1)
	retry.AttemptID = "retry-success"
	retry.StartedAt = rows[0].FinishedAt
	retry.FinishedAt = retry.StartedAt.Add(time.Millisecond)
	rows = append(rows, retry)
	got := AssessError(rows, nil, .05, time.Time{}, machineNow)
	if got.Clear != 50 || got.Failed != 0 || got.Verdict != VerdictPass {
		t.Fatalf("retry: %+v", got)
	}
	// The completed copy wins over the matching Begin observation.
	pending := retry
	pending.FinishedAt = time.Time{}
	if again := AssessError(rows, []AttemptSample{pending}, .05, time.Time{}, machineNow); !reflect.DeepEqual(again, got) {
		t.Fatal("Begin shadowed Finish")
	}
	// An even later, excluded final attempt hides both previous results.
	excluded := retry
	excluded.AttemptID = "excluded"
	excluded.StartedAt = retry.FinishedAt
	excluded.FinishedAt = excluded.StartedAt.Add(time.Millisecond)
	excluded.Outcome = OutcomeOther
	excluded.Class = ClassRequest
	got = AssessError(append(rows, excluded), nil, .05, time.Time{}, machineNow)
	if got.Clear != 49 || got.Failed != 0 {
		t.Fatalf("excluded final resurrected history: %+v", got)
	}
	// Unknown final result takes a slot, rather than preserving an older success.
	pending.StartedAt = excluded.FinishedAt
	pending.AttemptID = "running-retry"
	got = AssessError(rows, []AttemptSample{pending}, .05, time.Time{}, machineNow)
	if got.Clear != 49 || got.Unknown != 1 || got.Verdict != VerdictUnknown {
		t.Fatalf("running final: %+v", got)
	}
}

func TestAssessErrorSlotsAndExclusions(t *testing.T) {
	rows := machineSamples(101)
	running := make([]AttemptSample, 0, 51)
	for i := 50; i < len(rows); i++ {
		s := rows[i]
		s.FinishedAt = time.Time{}
		running = append(running, s)
	}
	got := AssessError(rows[:50], running, .05, time.Time{}, machineNow)
	if got.Clear != 49 || got.Unknown != 51 || got.Padded != 100 || got.Verdict != VerdictUnknown {
		t.Fatalf("101st request backfilled: %+v", got)
	}
	// Excluded classes do not consume slots.
	rows = machineSamples(150)
	excluded := []ErrorClass{ClassRequest, ClassMigration400, ClassCapability, ClassFirstOutputTimeout, ClassGateway, ClassClientCancel, ClassRepairStep}
	for i := 50; i < len(rows); i++ {
		rows[i].Outcome = OutcomeOther
		rows[i].Class = excluded[i%len(excluded)]
	}
	got = AssessError(rows, nil, .05, time.Time{}, machineNow)
	if got.Clear != 50 || got.Unknown != 0 || got.Verdict != VerdictPass {
		t.Fatalf("excluded classes consumed slots: %+v", got)
	}
	for c := ClassUpstream; c <= ClassAccountCapacity; c++ {
		rows := machineSamples(6)
		failSamples(rows, 6)
		for i := range rows {
			rows[i].Class = c
		}
		if got := AssessError(rows, nil, .05, time.Time{}, machineNow); got.Failed != 6 || got.Verdict != VerdictFail {
			t.Fatalf("class %s: %+v", c, got)
		}
	}
}

func TestAssessErrorCutWindowAndOrdering(t *testing.T) {
	rows := machineSamples(50)
	rows[0].FinishedAt = machineNow.Add(-ErrorWindow)
	rows[0].StartedAt = rows[0].FinishedAt.Add(-time.Second)
	if got := AssessError(rows, nil, .05, time.Time{}, machineNow); got.Clear != 50 {
		t.Fatal("window left endpoint excluded")
	}
	rows[0].FinishedAt = rows[0].FinishedAt.Add(-time.Nanosecond)
	if got := AssessError(rows, nil, .05, time.Time{}, machineNow); got.Clear != 49 {
		t.Fatal("expired request included")
	}
	rows = machineSamples(50)
	if got := AssessError(rows, nil, .05, rows[0].StartedAt, machineNow); got.Clear != 49 {
		t.Fatal("cut must be strict")
	}
	for i := range rows {
		rows[i].RequestID = ""
	}
	if got := AssessError(rows, nil, .05, time.Time{}, machineNow); got.Clear != 50 {
		t.Fatal("empty request IDs merged")
	}
	failSamples(rows, 50)
	before := append([]AttemptSample(nil), rows...)
	got := AssessError(rows, nil, .2, time.Time{}, machineNow)
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("mutated input")
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if other := AssessError(rows, nil, .2, time.Time{}, machineNow); !reflect.DeepEqual(got, other) {
		t.Fatal("input order changed assessment")
	}
	if got.AttemptIDs[0] != "attempt-049" {
		t.Fatal("bad attempts are not newest first")
	}
}

func TestObserveCountsOnlyAdmittedUnits(t *testing.T) {
	rows := machineSamples(104)
	for i := range rows {
		rows[i].StartedAt = machineNow.Add(time.Duration(i-105) * time.Second)
		rows[i].FinishedAt = rows[i].StartedAt.Add(time.Millisecond)
		rows[i].GroupID = 20
	}
	// Four old, local requests are outside the latest 100 slots.
	for i := range rows[:4] {
		rows[i].GroupID = 10
	}
	rows[103].GroupID, rows[103].Outcome, rows[103].Class = 10, OutcomeAccountFault, ClassUpstream
	rows[102].GroupID, rows[102].Outcome, rows[102].Class = 10, OutcomeOther, ClassUnknown
	assessment, local := AssessErrorForGroup(rows, nil, .05, time.Time{}, machineNow, 10)
	if local != 1 || assessment.Clear != 99 || assessment.Unknown != 1 {
		t.Fatalf("local=%d clear=%d unknown=%d", local, assessment.Clear, assessment.Unknown)
	}

	turns := machineSamples(4)
	turns[0].GroupID, turns[1].GroupID = 20, 20
	turns[2].CacheReadTokens = nil
	cache, local := AssessCacheForGroup(turns, .8, time.Time{}, machineNow, 10)
	if local != 1 || cache.Turns != 3 {
		t.Fatalf("cache local=%d total=%d", local, cache.Turns)
	}
}

func TestAssessErrorOnePercentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		total, failed int
		want          Verdict
	}{{50, 1, VerdictPass}, {50, 2, VerdictFail}, {100, 2, VerdictPass}, {100, 3, VerdictFail}, {1, 1, VerdictUnknown}, {2, 2, VerdictFail}} {
		t.Run(fmt.Sprintf("%d/%d", tc.failed, tc.total), func(t *testing.T) {
			rows := machineSamples(tc.total)
			failSamples(rows, tc.failed)
			for _, limit := range []float64{.01, 0, .009} {
				got := AssessError(rows, nil, limit, time.Time{}, machineNow)
				if got.Verdict != tc.want || got.Limit != .01 || got.Padded != max(50, tc.total) {
					t.Fatalf("assessment: %+v", got)
				}
			}
		})
	}
}
