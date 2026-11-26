//go:build unit

package schedquality

import (
	"testing"
	"time"
)

// F2: compose the actual review result, not a manually constructed trial view.
func TestStepAuditF2TrialPlacement(t *testing.T) {
	for _, line := range []Dimension{DimensionTTFO, DimensionError, DimensionCache} {
		t.Run(string(line), func(t *testing.T) {
			rear := machineState(line)
			rear.Identity, rear.Admissions = IdentityRear, 1
			rear.Entry, rear.LastRear = machineNow.Add(-20*time.Minute), machineNow.Add(-20*time.Minute)
			rear.Until = machineNow
			trial, action := machineStep(rear, EventReview, VerdictUnknown, machineNow)
			if trial.Identity != IdentityUnknown || !trial.Trial || action != ActionTrial {
				t.Fatalf("review with insufficient evidence: %+v / %s", trial, action)
			}
			lv := LineView{Identity: trial.Identity, Trial: trial.Trial, Level: trial.Level,
				Until: trial.Until, TrialUntil: trial.TrialUntil}
			var view StateView
			switch line {
			case DimensionTTFO:
				view.TTFO = lv
			case DimensionError:
				view.Error = lv
			case DimensionCache:
				view.Cache = lv
			}
			if got := view.Placement(true, machineNow); got != PlaceTrial {
				t.Fatalf("reviewed trial placement = %v, want Trial", got)
			}
		})
	}
}

// F3: seed a nonzero GoodSince so each reset assertion detects a missing write.
func TestStepAuditF3GoodSinceResets(t *testing.T) {
	for _, line := range []Dimension{DimensionTTFO, DimensionError, DimensionCache} {
		t.Run(string(line), func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				kind       EventKind
				verdict    Verdict
				identity   Identity
				trial      bool
				pending    bool
				wantAction Action
			}{
				{"ordinary-first-failure", EventRegular, VerdictFail, IdentityPass, false, false, ""},
				{"ordinary-confirmed-failure", EventRegular, VerdictFail, IdentityUnknown, false, true, ActionEnter},
				{"trial-failure", EventTrial, VerdictFail, IdentityUnknown, true, false, ActionTrialFail},
				{"enter-trial", EventReview, VerdictUnknown, IdentityRear, false, false, ActionTrial},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s := machineState(line)
					s.Identity, s.Trial = tc.identity, tc.trial
					s.Level, s.Admissions = 2, 3
					s.LastRear = machineNow.Add(-time.Hour)
					s.GoodSince = machineNow.Add(-20 * time.Minute)
					s.Until, s.TrialUntil = machineNow, machineNow.Add(time.Hour)
					if tc.pending {
						s.PendingAt = machineNow.Add(-RegularInterval)
					}
					next, action := machineStep(s, tc.kind, tc.verdict, machineNow)
					wantAction := tc.wantAction
					if tc.kind == EventRegular && line == DimensionTTFO {
						wantAction = ActionEnter
					}
					if action != wantAction || !next.GoodSince.IsZero() {
						t.Fatalf("GoodSince not cleared on %s: %+v / %s", tc.name, next, action)
					}
					if tc.kind == EventRegular && wantAction == "" &&
						(next.Identity != IdentityUnknown || !next.PendingAt.Equal(machineNow)) {
						t.Fatal("first error/cache failure must remain pending and Unknown")
					}
					if tc.kind == EventTrial && (next.Level != 0 || next.Admissions != 4 ||
						next.Until.Sub(machineNow) != RearDuration) {
						t.Fatalf("trial failure must use a fixed period and count exactly once: %+v", next)
					}
				})
			}
		})
	}
}

func TestStepNormalAssessmentDoesNotClearLegacyHistory(t *testing.T) {
	for _, level := range []int{0, 3} {
		s := machineState(DimensionTTFO)
		s.Level, s.Admissions = level, 4
		if _, due := Due(s, machineNow, false, false); due {
			t.Fatalf("normal history created an automatic clock at level %d", level)
		}
		if next, action := machineStep(s, EventRegular, VerdictUnknown, machineNow); action != "" || next.Level != level || next.Admissions != 4 {
			t.Fatalf("regular tick cleared legacy history: %+v / %s", next, action)
		}
	}
}

func TestStepFirstAdmissionRetainedAfterGoodEvidence(t *testing.T) {
	rear, _ := machineStep(machineState(DimensionTTFO), EventRegular, VerdictFail, machineNow)
	releasedAt := rear.Until
	s, _ := machineStep(rear, EventReview, VerdictPass, releasedAt)
	s, _ = machineStep(s, EventTrial, VerdictPass, releasedAt.Add(TrialMinimum))
	releasedAt = s.EvaluatedAt
	for tick := 1; tick <= 6; tick++ {
		next, action := machineStep(s, EventRegular, VerdictPass, releasedAt.Add(time.Duration(tick)*RegularInterval))
		if next.Level != 0 || next.Admissions != 1 || action != "" {
			t.Fatalf("first-admission history cleared before 30 minutes: %+v / %s", next, action)
		}
		s = next
	}
	at := s.EvaluatedAt.Add(RegularInterval)
	next, action := machineStep(s, EventRegular, VerdictFail, at)
	if action != ActionEnter || next.Level != 0 || next.Admissions != 2 || next.Until.Sub(at) != RearDuration {
		t.Fatalf("historical admission changed the next rear period: %+v / %s", next, action)
	}
}
