//go:build unit

package schedquality

import (
	"testing"
	"time"
)

func evaluationState() StateRow {
	return StateRow{AccountID: evaluationScope.AccountID, GroupID: evaluationScope.GroupID, Model: evaluationScope.Model}
}

func slowEvaluationSamples(n int, group int64, age time.Duration) []AttemptSample {
	rows := evaluationSamples(n, group, age)
	for i := range rows {
		rows[i].TTFOMs = sampleInt(2000)
	}
	return rows
}

func TestAdvanceCooldownLadder(t *testing.T) {
	for _, tc := range []struct {
		level, wantLevel int
		duration         time.Duration
	}{
		{0, 1, 20 * time.Minute}, {1, 2, 40 * time.Minute}, {2, 3, 60 * time.Minute},
		{3, 4, 90 * time.Minute}, {4, 5, 120 * time.Minute}, {5, 5, 120 * time.Minute},
	} {
		prev := evaluationState()
		prev.Level = tc.level
		prev.EvidenceSince = evaluationNow.Add(-15 * time.Minute)
		next, _ := AdvanceAt(prev, evaluationPolicy(), slowEvaluationSamples(20, 10, 10*time.Minute), evaluationNow, EvaluateDeadline)
		if next.State != StateCooling || next.Level != tc.wantLevel || next.Until.Sub(evaluationNow) != tc.duration || next.EvidenceSince != prev.EvidenceSince {
			t.Fatalf("level %d: %+v", tc.level, next)
		}
	}
}

func TestAdvanceNormalDecayBoundary(t *testing.T) {
	for _, tc := range []struct {
		normal   time.Duration
		level    int
		duration time.Duration
	}{
		{29 * time.Minute, 5, 120 * time.Minute}, {30 * time.Minute, 1, 20 * time.Minute},
	} {
		prev := evaluationState()
		prev.Level, prev.EvidenceSince = 4, evaluationNow.Add(-tc.normal)
		next, _ := AdvanceAt(prev, evaluationPolicy(), slowEvaluationSamples(20, 10, 10*time.Minute), evaluationNow, EvaluateDeadline)
		if next.Level != tc.level || next.Until.Sub(evaluationNow) != tc.duration {
			t.Fatalf("normal %v: %+v", tc.normal, next)
		}
	}
}

func TestAdvanceDeadlineException(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  func() []AttemptSample
		renew bool
	}{
		{"local-failures-do-not-extend", func() []AttemptSample { return slowEvaluationSamples(20, 10, 10*time.Minute) }, false},
		{"fresh-foreign-failures", func() []AttemptSample { return slowEvaluationSamples(20, 20, 10*time.Minute) }, true},
		{"old-foreign-failures", func() []AttemptSample { return slowEvaluationSamples(20, 20, 30*time.Minute) }, false},
		{"old-batch-plus-one-new-failure", func() []AttemptSample {
			return append(slowEvaluationSamples(20, 20, 30*time.Minute), slowEvaluationSamples(1, 20, time.Minute)...)
		}, true},
		{"old-batch-plus-one-new-success", func() []AttemptSample {
			return append(slowEvaluationSamples(20, 20, 30*time.Minute), evaluationSamples(1, 20, time.Minute)...)
		}, true},
		{"foreign-slow-under-target-line", func() []AttemptSample { return evaluationSamples(20, 20, 10*time.Minute) }, false},
		{"new-foreign-fast-path", func() []AttemptSample { return slowEvaluationSamples(6, 20, time.Minute) }, true},
		{"completed-at-round-start-is-old", func() []AttemptSample {
			rows := slowEvaluationSamples(20, 20, 20*time.Minute)
			for i := range rows {
				rows[i].FinishedAt = evaluationNow.Add(-20 * time.Minute)
				rows[i].StartedAt = rows[i].FinishedAt.Add(-time.Second)
			}
			return rows
		}, false},
		{"started-before-round-completed-during-round", func() []AttemptSample {
			rows := slowEvaluationSamples(20, 20, 10*time.Minute)
			for i := range rows {
				rows[i].StartedAt = evaluationNow.Add(-30 * time.Minute)
			}
			return rows
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := evaluationState()
			prev.State, prev.Level = StateCooling, 5
			prev.Since, prev.Until, prev.EvidenceSince = evaluationNow.Add(-20*time.Minute), evaluationNow, evaluationNow.Add(-2*time.Hour)
			next, e := AdvanceAt(prev, evaluationPolicy(), tc.rows(), evaluationNow, EvaluateDeadline)
			if tc.renew {
				if next.State != StateCooling || next.Level != 5 || next.Until.Sub(evaluationNow) != 120*time.Minute || next.EvidenceSince != prev.EvidenceSince || e.Speed.Source != EvidenceOther {
					t.Fatalf("expected renewal: %+v / %+v", next, e)
				}
			} else if next.State != StateNormal || next.Level != 5 || next.EvidenceSince != evaluationNow || !next.Until.IsZero() {
				t.Fatalf("expected timed release: %+v", next)
			}
		})
	}
}

func TestAdvancePreservesEvidenceAndCannotReuseRenewal(t *testing.T) {
	prev := evaluationState()
	prev.EvidenceSince = evaluationNow.Add(-2 * time.Hour)
	rows := slowEvaluationSamples(20, 20, 10*time.Minute)
	entered, _ := AdvanceAt(prev, evaluationPolicy(), rows, evaluationNow.Add(-5*time.Minute), EvaluateDeadline)
	if entered.State != StateCooling {
		t.Fatal("fixture must enter")
	}
	// Old evidence still contributes to ordinary evaluation while cooling.
	current, e := AdvanceAt(entered, evaluationPolicy(), rows, evaluationNow, EvaluateDeadline)
	if current != entered || e.Speed.N != 20 {
		t.Fatal("entry cleared evidence")
	}
	fresh := slowEvaluationSamples(20, 20, time.Minute)
	renewed, _ := AdvanceAt(entered, evaluationPolicy(), fresh, entered.Until, EvaluateDeadline)
	if renewed.Level != 2 || renewed.EvidenceSince != prev.EvidenceSince {
		t.Fatalf("renewal = %+v", renewed)
	}
	released, _ := AdvanceAt(renewed, evaluationPolicy(), fresh, renewed.Until, EvaluateDeadline)
	if released.State != StateNormal {
		t.Fatalf("same evidence renewed twice: %+v", released)
	}
}

func TestAdvanceTimedReleaseClearsEvidence(t *testing.T) {
	prev := evaluationState()
	prev.State, prev.Level = StateCooling, 3
	prev.Since, prev.Until = evaluationNow.Add(-time.Hour), evaluationNow
	prev.EvidenceSince = evaluationNow.Add(-time.Hour)
	rows := slowEvaluationSamples(20, 10, time.Minute)
	before, _ := AdvanceAt(prev, evaluationPolicy(), rows, evaluationNow.Add(-time.Nanosecond), EvaluateDeadline)
	if before != prev {
		t.Fatal("released before deadline")
	}
	released, _ := AdvanceAt(prev, evaluationPolicy(), rows, evaluationNow, EvaluateDeadline)
	if released.State != StateNormal || released.EvidenceSince != evaluationNow || !released.Until.IsZero() {
		t.Fatalf("timed release = %+v", released)
	}
	next, _ := AdvanceAt(released, evaluationPolicy(), rows, evaluationNow.Add(time.Second), EvaluateDeadline)
	if next.State != StateNormal || next.EvidenceSince != evaluationNow {
		t.Fatal("old evidence survived timed release")
	}
}

func TestAdvanceExemptionOnlyChangesPlacement(t *testing.T) {
	next, e := AdvanceAt(evaluationState(), evaluationPolicy(), slowEvaluationSamples(6, 10, time.Minute), evaluationNow, EvaluateDeadline)
	v := StateView{Failing: next.State == StateCooling, Level: next.Level,
		GroupOverride: OverrideRef{Mode: OverrideExempt, Expires: evaluationNow.Add(time.Minute)}}
	if next.State != StateCooling || e.Verdicts.Speed != VerdictFail || StandingOf(v, true, evaluationNow) != StandFront || StandingOf(v, true, evaluationNow.Add(time.Minute)) != StandBack {
		t.Fatal("exemption must preserve internal state and expire at its deadline")
	}
}
