//go:build unit

package schedquality

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestAssessErrorSourceGroupsUseSelectedRequestUnits(t *testing.T) {
	cut := machineNow.Add(-10 * time.Minute)
	rows := machineSamples(101)
	for i := range rows {
		rows[i].GroupID = 40
	}
	rows[0].GroupID = 99 // The 101st request must not contribute a source group.
	rows[100].GroupID, rows[100].Outcome, rows[100].Class = 10, OutcomeAccountFault, ClassUpstream
	rows[99].GroupID, rows[99].Outcome, rows[99].Class = 30, OutcomeOther, ClassUnknown
	running := rows[98]
	running.GroupID, running.FinishedAt = 20, time.Time{}
	rows = append(rows[:98], rows[99:]...)
	// The final attempt's group contributes, not a superseded attempt's group.
	earlier := rows[5]
	earlier.AttemptID, earlier.GroupID = "superseded-attempt", 77
	earlier.FinishedAt = earlier.StartedAt
	earlier.StartedAt = earlier.StartedAt.Add(-time.Second)
	excluded := rows[1]
	excluded.AttemptID, excluded.RequestID, excluded.GroupID = "excluded", "excluded-request", 90
	excluded.Outcome, excluded.Class = OutcomeOther, ClassRequest
	preCut := rows[1]
	preCut.AttemptID, preCut.RequestID, preCut.GroupID = "pre-cut", "pre-cut-request", 80
	preCut.StartedAt, preCut.FinishedAt = cut.Add(-10*time.Second), cut.Add(-time.Second)
	retry := preCut
	retry.AttemptID, retry.GroupID = "post-cut-retry", 81
	retry.StartedAt, retry.FinishedAt = cut.Add(time.Second), cut.Add(2*time.Second)
	rows = append(rows, earlier, excluded, preCut, retry)
	want := []int64{10, 20, 30, 40}
	for pass := 0; pass < 2; pass++ {
		out := AssessError(rows, []AttemptSample{running}, .05, cut, machineNow)
		if out.Clear != 98 || out.Failed != 1 || out.Unknown != 2 || !reflect.DeepEqual(out.SourceGroups, want) {
			t.Fatalf("sources must come from the 100 selected final units, including success and unknown: %+v", out)
		}
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
}

func TestAssessCacheSourceGroupsUseSelectedTurns(t *testing.T) {
	cut := machineNow.Add(-5 * time.Minute)
	rows := machineSamples(20)
	for i := range rows {
		rows[i].GroupID = 40
	}
	rows[0].GroupID, rows[0].CacheReadTokens = 30, machineInt(0)
	rows[1].GroupID = 10
	for i, change := range []func(*AttemptSample){
		func(s *AttemptSample) { s.Continuation = false },
		func(s *AttemptSample) { s.CacheReadTokens = nil },
		func(s *AttemptSample) { s.Outcome = OutcomeAccountFault },
		func(s *AttemptSample) {
			s.StartedAt, s.FinishedAt = machineNow.Add(-time.Hour-time.Second), machineNow.Add(-time.Hour)
		},
		func(s *AttemptSample) { s.StartedAt = cut },
		func(s *AttemptSample) { s.FinishedAt = machineNow.Add(time.Second) },
	} {
		s := rows[2]
		s.AttemptID, s.GroupID = fmt.Sprintf("ineligible-%d", i), int64(80+i)
		change(&s)
		rows = append(rows, s)
	}
	want := []int64{10, 30, 40}
	out := AssessCache(rows, .8, cut, machineNow)
	if out.Turns != 20 || !reflect.DeepEqual(out.SourceGroups, want) {
		t.Fatalf("sources must include every selected turn, not just low-cache evidence: %+v", out)
	}
}
