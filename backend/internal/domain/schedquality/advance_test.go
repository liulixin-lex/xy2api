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
		prev.EvidenceSince = evaluationNow.Add(-time.Hour)
		next, _ := Advance(prev, evaluationPolicy(), slowEvaluationSamples(20, 10, 10*time.Minute), evaluationNow)
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
		prev.Level, prev.Since = 4, evaluationNow.Add(-tc.normal)
		next, _ := Advance(prev, evaluationPolicy(), slowEvaluationSamples(20, 10, 10*time.Minute), evaluationNow)
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
		}, false},
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
			prev.State, prev.Level, prev.Reason = StateCooling, 5, ReasonSpeed
			prev.Since, prev.Until, prev.EvidenceSince = evaluationNow.Add(-20*time.Minute), evaluationNow, evaluationNow.Add(-2*time.Hour)
			next, e := Advance(prev, evaluationPolicy(), tc.rows(), evaluationNow)
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
	entered, _ := Advance(prev, evaluationPolicy(), rows, evaluationNow.Add(-5*time.Minute))
	if entered.State != StateCooling {
		t.Fatal("fixture must enter")
	}
	// Old evidence still contributes to ordinary evaluation while cooling.
	current, e := Advance(entered, evaluationPolicy(), rows, evaluationNow)
	if current != entered || e.Speed.N != 20 {
		t.Fatal("entry cleared evidence")
	}
	fresh := slowEvaluationSamples(20, 20, time.Minute)
	renewed, _ := Advance(entered, evaluationPolicy(), fresh, entered.Until)
	if renewed.Level != 2 || renewed.EvidenceSince != prev.EvidenceSince {
		t.Fatalf("renewal = %+v", renewed)
	}
	released, _ := Advance(renewed, evaluationPolicy(), fresh, renewed.Until)
	if released.State != StateNormal {
		t.Fatalf("same evidence renewed twice: %+v", released)
	}
}

func TestAdvanceTimedReleaseAndManualRestore(t *testing.T) {
	prev := evaluationState()
	prev.State, prev.Level, prev.Reason = StateCooling, 3, ReasonSpeed
	prev.Since, prev.Until = evaluationNow.Add(-time.Hour), evaluationNow
	rows := slowEvaluationSamples(20, 10, time.Minute)
	before, _ := Advance(prev, evaluationPolicy(), rows, evaluationNow.Add(-time.Nanosecond))
	if before != prev {
		t.Fatal("released before deadline")
	}
	released, _ := Advance(prev, evaluationPolicy(), rows, evaluationNow)
	restored := Restore(prev, evaluationNow)
	for _, state := range []StateRow{released, restored} {
		next, _ := Advance(state, evaluationPolicy(), rows, evaluationNow.Add(time.Second))
		if next.State != StateNormal || next.EvidenceSince != evaluationNow {
			t.Fatal("old evidence survived release/reset")
		}
	}
	if restored.Level != 0 || restored.Reason != "" || restored.State != StateNormal || !restored.Until.IsZero() {
		t.Fatalf("manual restore = %+v", restored)
	}
}

func TestAdvanceExemptionOnlyChangesPlacement(t *testing.T) {
	next, e := Advance(evaluationState(), evaluationPolicy(), slowEvaluationSamples(6, 10, time.Minute), evaluationNow)
	v := StateView{State: next.State, Level: next.Level, Reason: next.Reason, Verdicts: e.Verdicts,
		GroupOverride: OverrideRef{Mode: OverrideExempt, Expires: evaluationNow.Add(time.Minute)}}
	if next.State != StateCooling || e.Verdicts.Speed != VerdictFail || StandingOf(v, true, evaluationNow) != StandFront || StandingOf(v, true, evaluationNow.Add(time.Minute)) != StandBack {
		t.Fatal("exemption must preserve internal state and expire at its deadline")
	}
}
