//go:build unit

package schedquality

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Red review #1: all six requests predate entry even though their retries do not.
func TestRequestUnitsReviewUsesFirstStart(t *testing.T) {
	cut := machineNow.Add(-20 * time.Minute)
	rows := machineSamples(6)
	failSamples(rows, 6)
	for i := range rows {
		rows[i].StartedAt = cut.Add(-10 * time.Second)
		rows[i].FinishedAt = cut.Add(-time.Second)
	}
	for _, first := range rows[:6] {
		retry := first
		retry.AttemptID += "-retry"
		retry.StartedAt, retry.FinishedAt = cut.Add(time.Second), cut.Add(2*time.Second)
		rows = append(rows, retry)
	}
	assessment := AssessError(rows, nil, .05, cut, machineNow)
	rear := machineState(DimensionError)
	rear.Identity, rear.Admissions = IdentityRear, 1
	rear.Entry, rear.LastRear, rear.EvaluatedAt = cut, cut, cut
	rear.Until = machineNow
	next, action := Step(rear, EventReview, assessment.Verdict, Basis{Error: &assessment}, machineNow)
	t.Logf("clear=%d failed=%d verdict=%s action=%s identity=%d trial=%t level=%d until_delta=%s",
		assessment.Clear, assessment.Failed, assessment.Verdict, action, next.Identity,
		next.Trial, next.Level, next.Until.Sub(machineNow))
	if assessment.Clear != 0 || assessment.Failed != 0 || assessment.Verdict != VerdictUnknown {
		t.Errorf("pre-entry requests re-entered review: %+v", assessment)
	}
	if action != ActionTrial || next.Identity != IdentityUnknown || !next.Trial || next.Level != 0 {
		t.Errorf("old requests renewed rear instead of starting trial: %+v / %s", next, action)
	}
	if !next.Epoch.Equal(machineNow) || !next.TrialUntil.Equal(machineNow.Add(TrialDuration)) {
		t.Error("review did not establish a fresh trial epoch and deadline")
	}
}

// Red review #2: the later finish takes precedence over the lexical attempt ID.
func TestRequestUnitsSameStartLaterSuccess(t *testing.T) {
	rows := machineSamples(50)
	failSamples(rows, 6)
	rows[0].AttemptID = "z-first-failure"
	retry := rows[0]
	retry.AttemptID = "a-final-success"
	retry.FinishedAt = retry.FinishedAt.Add(time.Millisecond)
	retry.Outcome, retry.Class = OutcomeOK, ClassNone
	rows = append(rows, retry)
	for _, order := range []string{"forward", "reverse"} {
		t.Run(order, func(t *testing.T) {
			assessment := AssessError(rows, nil, .05, time.Time{}, machineNow)
			t.Logf("clear=%d failed=%d verdict=%s", assessment.Clear, assessment.Failed, assessment.Verdict)
			if assessment.Clear != 50 || assessment.Failed != 5 || assessment.Verdict != VerdictPass {
				t.Errorf("later successful completion ignored: %+v", assessment)
			}
			summary := SummarizeAccount(rows, 1, time.Time{}, machineNow)
			if summary.ErrorN != 50 || summary.ErrorBad != 5 {
				t.Errorf("summary used different final request results: %+v", summary)
			}
		})
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
}

func TestRequestUnitsSummaryResetUsesFirstStart(t *testing.T) {
	reset := machineNow.Add(-time.Minute)
	for _, offset := range []time.Duration{-10 * time.Second, 0} {
		t.Run(offset.String(), func(t *testing.T) {
			first := machineSamples(1)[0]
			first.StartedAt, first.FinishedAt = reset.Add(offset), reset.Add(time.Second)
			first.Outcome, first.Class = OutcomeAccountFault, ClassUpstream
			retry := first
			retry.AttemptID = "successful-retry"
			retry.StartedAt, retry.FinishedAt = reset.Add(2*time.Second), reset.Add(3*time.Second)
			retry.Outcome, retry.Class = OutcomeOK, ClassNone
			rows := []AttemptSample{retry, first}
			out := SummarizeAccount(rows, 1, reset, machineNow)
			if out.ErrorN != 0 || out.ErrorBad != 0 {
				t.Fatalf("pre-reset request returned through its retry: %+v", out)
			}
			out = SummarizeAccount(rows, 1, time.Time{}, machineNow)
			if out.ErrorN != 1 || out.ErrorBad != 0 {
				t.Fatalf("unreset summary lost final success: %+v", out)
			}
		})
	}
}

func TestRequestUnitsRunningAgeUsesFirstStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		unknown int
	}{
		{"expired", ErrorWindow + time.Nanosecond, 0},
		{"inclusive-window-boundary", ErrorWindow, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := machineSamples(1)[0]
			first.StartedAt = machineNow.Add(-tc.age)
			first.FinishedAt = first.StartedAt.Add(time.Millisecond)
			first.Outcome, first.Class = OutcomeOther, ClassRepairStep
			running := first
			running.AttemptID = "running-retry"
			running.StartedAt, running.FinishedAt = machineNow.Add(-time.Second), time.Time{}
			out := AssessError([]AttemptSample{first}, []AttemptSample{running}, .05, time.Time{}, machineNow)
			if out.Clear != 0 || out.Unknown != tc.unknown {
				t.Fatalf("running request used retry age: %+v", out)
			}
		})
	}
	// Age also controls ordering: a new retry must not jump into the newest 100.
	rows := machineSamples(100)
	first := rows[0]
	first.RequestID, first.AttemptID = "old-request", "old-first"
	first.StartedAt, first.FinishedAt = machineNow.Add(-30*time.Minute), machineNow.Add(-29*time.Minute)
	running := first
	running.AttemptID = "old-retry"
	running.StartedAt, running.FinishedAt = machineNow, time.Time{}
	out := AssessError(append(rows, first), []AttemptSample{running}, .05, time.Time{}, machineNow)
	if out.Clear != 100 || out.Unknown != 0 || out.Verdict != VerdictPass {
		t.Fatalf("new retry displaced a newer request slot: %+v", out)
	}
}

func TestRequestUnitsBeginFinishAndTies(t *testing.T) {
	failed := machineSamples(1)[0]
	failed.AttemptID = "z-first-failure"
	failed.Outcome, failed.Class = OutcomeAccountFault, ClassUpstream
	succeeded := failed
	succeeded.AttemptID = "a-final-success"
	succeeded.FinishedAt = succeeded.FinishedAt.Add(time.Millisecond)
	succeeded.Outcome, succeeded.Class = OutcomeOK, ClassNone
	begin := failed
	begin.FinishedAt = time.Time{}
	// Complete copies must be reconciled before selecting between attempts.
	// Otherwise an older Begin can hide the later success depending on order.
	observations := []AttemptSample{failed, succeeded, begin}
	for _, indices := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(indices), func(t *testing.T) {
			rows := []AttemptSample{observations[indices[0]], observations[indices[1]], observations[indices[2]]}
			out := AssessError(rows, []AttemptSample{begin}, .05, time.Time{}, machineNow)
			if out.Clear != 1 || out.Failed != 0 || out.Unknown != 0 {
				t.Fatalf("Begin superseded its Finish or later success: %+v", out)
			}
		})
	}
	begin.AttemptID = "0-distinct-running"
	out := AssessError([]AttemptSample{failed, succeeded}, []AttemptSample{begin}, .05, time.Time{}, machineNow)
	if out.Unknown != 1 || out.Clear != 0 {
		t.Fatalf("distinct unfinished attempt did not sort after equal-start completions: %+v", out)
	}
	begin.StartedAt = begin.StartedAt.Add(-time.Nanosecond)
	out = AssessError([]AttemptSample{failed, succeeded}, []AttemptSample{begin}, .05, time.Time{}, machineNow)
	if out.Clear != 1 || out.Failed != 0 || out.Unknown != 0 {
		t.Fatalf("older unfinished attempt superseded a later start: %+v", out)
	}
	// AttemptID breaks only a full time tie; retain the actual final bad ID.
	failed.FinishedAt = succeeded.FinishedAt
	out = AssessError([]AttemptSample{succeeded, failed}, nil, .05, time.Time{}, machineNow)
	if out.Failed != 1 || !reflect.DeepEqual(out.AttemptIDs, []string{"z-first-failure"}) {
		t.Fatalf("last tie-break or bad attempt identity: %+v", out)
	}
}

func TestRequestUnitsFutureCompletionIsRunning(t *testing.T) {
	rows := machineSamples(50)
	failSamples(rows, 6)
	var future, running []AttemptSample
	for _, first := range rows[:6] {
		retry := first
		retry.AttemptID += "-future-success"
		retry.StartedAt = machineNow.Add(-time.Second)
		retry.FinishedAt = machineNow.Add(5 * time.Second)
		retry.Outcome, retry.Class = OutcomeOK, ClassNone
		future = append(future, retry)
		retry.FinishedAt = time.Time{}
		running = append(running, retry)
	}
	before := append([]AttemptSample(nil), future...)
	want := AssessError(rows, running, .05, time.Time{}, machineNow)
	got := AssessError(append(rows, future...), nil, .05, time.Time{}, machineNow)
	t.Logf("future-completion clear=%d failed=%d unknown=%d verdict=%s", got.Clear, got.Failed, got.Unknown, got.Verdict)
	if got.Clear != 44 || got.Failed != 0 || got.Unknown != 6 || got.Verdict != VerdictUnknown || !reflect.DeepEqual(got, want) {
		t.Fatalf("future final result resurrected its older failure: got %+v, running %+v", got, want)
	}
	if !reflect.DeepEqual(future, before) {
		t.Fatal("caller-owned future samples were changed")
	}
	// At the finish boundary the same records become successful final results.
	got = AssessError(append(rows, future...), nil, .05, time.Time{}, machineNow.Add(5*time.Second))
	if got.Clear != 50 || got.Failed != 0 || got.Unknown != 0 || got.Verdict != VerdictPass {
		t.Fatalf("completed-at-now records stayed unknown: %+v", got)
	}
	for i := range future {
		future[i].StartedAt = machineNow.Add(time.Nanosecond)
	}
	got = AssessError(append(rows, future...), nil, .05, time.Time{}, machineNow)
	if got.Clear != 50 || got.Failed != 6 || got.Unknown != 0 || got.Verdict != VerdictFail {
		t.Fatalf("not-yet-started retries altered the current result: %+v", got)
	}
}

func TestRequestUnitsUnclassifiedFailureIsUnknown(t *testing.T) {
	rows := machineSamples(50)
	for i := 0; i < 6; i++ {
		rows[i].Outcome = OutcomeAccountFault
	}
	out := AssessError(rows, nil, .05, time.Time{}, machineNow)
	if out.Clear != 44 || out.Failed != 0 || out.Unknown != 6 || out.Verdict != VerdictUnknown {
		t.Fatalf("unclassified failure did not occupy an unknown slot: %+v", out)
	}
	for i := range rows {
		rows[i].Outcome = OutcomeOK
	}
	out = AssessError(rows, nil, .05, time.Time{}, machineNow)
	if out.Clear != 50 || out.Failed != 0 || out.Unknown != 0 || out.Verdict != VerdictPass {
		t.Fatalf("successful ClassNone was not success: %+v", out)
	}
}

func TestRequestUnitsCompletedSlotsUseFinishTime(t *testing.T) {
	rows := machineSamples(101)
	// This request started first but finished last; it must displace attempt-001.
	rows[0].StartedAt, rows[0].FinishedAt = machineNow.Add(-30*time.Minute), machineNow
	rows[0].Outcome, rows[0].Class = OutcomeAccountFault, ClassUpstream
	rows[1].Outcome, rows[1].Class = OutcomeOther, ClassUnknown
	out := AssessError(rows, nil, .05, time.Time{}, machineNow)
	if out.Clear != 100 || out.Failed != 1 || out.Unknown != 0 ||
		!reflect.DeepEqual(out.AttemptIDs, []string{"attempt-000"}) {
		t.Fatalf("long completed request sorted by start instead of finish: %+v", out)
	}
	// Before completion its old start is the ordering time, so it drops out.
	rows[0].FinishedAt = time.Time{}
	out = AssessError(rows[1:], rows[:1], .05, time.Time{}, machineNow)
	if out.Clear != 99 || out.Failed != 0 || out.Unknown != 1 || len(out.AttemptIDs) != 0 {
		t.Fatalf("running request did not sort by its start: %+v", out)
	}
}
