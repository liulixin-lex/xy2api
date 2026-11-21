//go:build unit

package schedquality

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func machineState(line Dimension) LineState {
	return Relaxed(LineKey{AccountID: 1, Model: "synthetic-model", GroupID: 10, Line: line})
}
func machineStep(s LineState, kind EventKind, v Verdict, at time.Time) (LineState, Action) {
	return Step(s, kind, v, Basis{RuleVersion: RuleVersion, Line: s.Line, Threshold: .05}, at)
}

func TestStepOrdinaryConfirmation(t *testing.T) {
	for _, line := range []Dimension{DimensionTTFO, DimensionError, DimensionCache} {
		t.Run(string(line), func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				verdicts []Verdict
				minutes  []int
				wantRear bool
			}{
				{"first", []Verdict{VerdictFail}, []int{0}, line == DimensionTTFO},
				{"adjacent", []Verdict{VerdictFail, VerdictFail}, []int{0, 5}, true},
				{"unknown-breaks", []Verdict{VerdictFail, VerdictUnknown, VerdictFail}, []int{0, 5, 10}, line == DimensionTTFO},
				{"pass-breaks", []Verdict{VerdictFail, VerdictPass, VerdictFail}, []int{0, 5, 10}, line == DimensionTTFO},
				{"missing-tick", []Verdict{VerdictFail, VerdictFail}, []int{0, 10}, line == DimensionTTFO},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s := machineState(line)
					s.Identity = IdentityPass
					s.GoodSince = machineNow.Add(-20 * time.Minute)
					for i, v := range tc.verdicts {
						s, _ = machineStep(s, EventRegular, v, machineNow.Add(time.Duration(tc.minutes[i])*time.Minute))
					}
					if (s.Identity == IdentityRear) != tc.wantRear {
						t.Fatalf("state %+v", s)
					}
					if !tc.wantRear && (s.Identity != IdentityUnknown || s.PendingAt.IsZero() || !s.GoodSince.IsZero()) {
						t.Fatalf("pending state %+v", s)
					}
				})
			}
		})
	}
}

func TestStepCooldownAndReview(t *testing.T) {
	s, action := machineStep(machineState(DimensionTTFO), EventRegular, VerdictFail, machineNow)
	if action != ActionEnter || s.Level != 0 || s.Admissions != 1 || s.Until.Sub(machineNow) != 20*time.Minute {
		t.Fatalf("first entry: %+v/%s", s, action)
	}
	frozen := s
	if early, a := machineStep(s, EventReview, VerdictPass, s.Until.Add(-time.Nanosecond)); a != "" || !reflect.DeepEqual(early, s) {
		t.Fatal("early release")
	}
	for _, tc := range []struct {
		v      Verdict
		action Action
		id     Identity
		trial  bool
		level  int
	}{
		{VerdictFail, ActionRenew, IdentityRear, false, 1},
		{VerdictPass, ActionTrial, IdentityUnknown, true, 0},
		{VerdictUnknown, ActionTrial, IdentityUnknown, true, 0},
	} {
		next, a := machineStep(frozen, EventReview, tc.v, frozen.Until)
		if a != tc.action || next.Identity != tc.id || next.Trial != tc.trial || next.Level != tc.level {
			t.Fatalf("review: %+v/%s", next, a)
		}
		if tc.trial && (!next.Epoch.Equal(frozen.Until) || !next.TrialUntil.Equal(frozen.Until.Add(TrialDuration))) {
			t.Fatal("trial cut/deadline")
		}
		if tc.v == VerdictFail && (!next.Entry.Equal(frozen.Until) || !next.LastRear.Equal(frozen.Until)) {
			t.Fatal("renewal did not restart evidence")
		}
		if tc.v == VerdictPass && !next.GoodSince.IsZero() {
			t.Fatal("review must enter trial without a good streak")
		}
	}
	for level := 1; level <= 6; level++ {
		at := s.Until
		s, action = machineStep(s, EventReview, VerdictFail, at)
		want := min(level, 4)
		if action != ActionRenew || s.Level != want || s.Until.Sub(at) != CooldownLadder[want] {
			t.Fatalf("ladder: %+v", s)
		}
	}
	s, _ = machineStep(s, EventReview, VerdictPass, s.Until)
	s, _ = machineStep(s, EventTrial, VerdictPass, s.Epoch.Add(TrialMinimum))
	s, action = machineStep(s, EventRegular, VerdictFail, s.EvaluatedAt.Add(RegularInterval))
	if action != ActionEnter || s.Level != 4 || s.Admissions != 2 {
		t.Fatal("later entry must escalate and count")
	}
}

func TestStepTrialDeadlineEvidenceWins(t *testing.T) {
	rear, _ := machineStep(machineState(DimensionTTFO), EventRegular, VerdictFail, machineNow)
	trial, _ := machineStep(rear, EventReview, VerdictUnknown, rear.Until)
	for _, line := range []Dimension{DimensionTTFO, DimensionError, DimensionCache} {
		for _, tc := range []struct {
			v  Verdict
			a  Action
			id Identity
		}{
			{VerdictFail, ActionTrialFail, IdentityRear}, {VerdictPass, ActionTrialPass, IdentityUnknown}, {VerdictUnknown, ActionTrialTimeout, IdentityUnknown},
		} {
			prev := trial
			prev.Line = line
			next, a := machineStep(prev, EventTrial, tc.v, prev.TrialUntil)
			if a != tc.a || next.Identity != tc.id || next.Trial || !next.Epoch.Equal(prev.TrialUntil) {
				t.Fatalf("deadline %+v/%s", next, a)
			}
			if tc.v == VerdictUnknown && (next.Level != prev.Level || next.Admissions != prev.Admissions) {
				t.Fatal("timeout lost history")
			}
			if tc.v == VerdictFail && (next.Level != 1 || next.Admissions != 2 || !next.LastRear.Equal(prev.TrialUntil)) {
				t.Fatal("trial failed to escalate")
			}
			if tc.v == VerdictPass && !next.GoodSince.IsZero() {
				t.Fatal("trial pass must start good evidence at the actual pass time")
			}
		}
	}
	s, a := machineStep(trial, EventTrial, VerdictUnknown, trial.EvaluatedAt.Add(time.Minute))
	if a != "" || !s.Trial {
		t.Fatal("unknown ended trial early")
	}
}

// Acceptance 4: the local step cannot clear shared history, even after 24h.
func TestStepLevelClearBoundaries(t *testing.T) {
	s := machineState(DimensionTTFO)
	s.Level, s.Admissions, s.LastRear = 2, 3, machineNow.Add(-LevelClearQuiet)
	for i := 0; i <= 6; i++ {
		next, a := machineStep(s, EventRegular, VerdictPass, machineNow.Add(time.Duration(i)*RegularInterval))
		if next.Level != 2 || next.Admissions != 3 || a != "" {
			t.Fatalf("local history clear: %+v/%s", next, a)
		}
		s = next
	}
	next, a := machineStep(s, EventRegular, VerdictFail, s.EvaluatedAt.Add(RegularInterval))
	if next.Level != 3 || a != ActionEnter {
		t.Fatal("good streak lost history")
	}
}

func TestDueCutAndIdempotence(t *testing.T) {
	base := machineState(DimensionTTFO)
	rear, _ := machineStep(base, EventRegular, VerdictFail, machineNow)
	trial, _ := machineStep(rear, EventReview, VerdictUnknown, rear.Until)
	for _, tc := range []struct {
		s           LineState
		at          time.Time
		five, touch bool
		kind        EventKind
		due         bool
	}{
		{base, machineNow, false, true, EventClock, false}, {base, machineNow, true, false, EventRegular, true},
		{rear, rear.Until.Add(-time.Second), true, true, EventReview, false}, {rear, rear.Until, true, true, EventReview, true},
		{trial, trial.EvaluatedAt.Add(time.Second), false, false, EventTrial, false}, {trial, trial.EvaluatedAt.Add(time.Second), false, true, EventTrial, true},
		{trial, trial.TrialUntil, false, false, EventTrial, true},
	} {
		kind, due := Due(tc.s, tc.at, tc.five, tc.touch)
		if due != tc.due || (due && kind != tc.kind) {
			t.Fatalf("due %v/%v", kind, due)
		}
	}
	for _, at := range []time.Time{rear.EvaluatedAt, rear.EvaluatedAt.Add(-time.Second)} {
		if _, due := Due(rear, at, true, true); due {
			t.Fatal("duplicate timestamp due")
		}
		if next, a := machineStep(rear, EventReview, VerdictPass, at); a != "" || !reflect.DeepEqual(next, rear) {
			t.Fatal("duplicate step changed state")
		}
	}
	if !rear.Cut(EventReview).Equal(rear.Entry) || !trial.Cut(EventTrial).Equal(trial.Epoch) {
		t.Fatal("cut mapping")
	}
	relaxed := Relaxed(rear.LineKey)
	if relaxed.LineKey != rear.LineKey || !reflect.DeepEqual(relaxed, base) {
		t.Fatal("relax retained history")
	}
	// Front identity changes persist with no decision action.
	pass, a := machineStep(base, EventRegular, VerdictPass, machineNow)
	if a != "" || pass.Identity != IdentityPass {
		t.Fatal("front transition emitted action")
	}
	unknown, a := machineStep(pass, EventRegular, VerdictUnknown, machineNow.Add(RegularInterval))
	if a != "" || unknown.Identity != IdentityUnknown || unknown.Basis == nil {
		t.Fatal("front unknown transition")
	}
}

func TestStepFrozenRuleBVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/machine/states.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name              string
		At                float64
		FiveMin           bool `json:"five_min"`
		Verdict, Identity int
		Before            struct {
			Identity            int
			Trial               bool
			Epoch, Entry, Until float64
			TrialUntil          float64 `json:"trial_until"`
		}
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 132 {
		t.Fatalf("vectors %d", len(cases))
	}
	stamp := func(v float64) time.Time {
		if v <= 0 {
			return time.Time{}
		}
		return time.Unix(0, int64(v*1e9)).UTC()
	}
	identity := func(v int) Identity {
		switch v {
		case 0:
			return IdentityPass
		case 1:
			return IdentityRear
		}
		return IdentityUnknown
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			s := machineState(DimensionTTFO)
			s.Identity = identity(tc.Before.Identity)
			s.Trial = tc.Before.Trial
			s.Epoch = stamp(tc.Before.Epoch)
			s.Entry = stamp(tc.Before.Entry)
			s.Until = stamp(tc.Before.Until)
			s.TrialUntil = stamp(tc.Before.TrialUntil)
			v := VerdictUnknown
			if tc.Verdict == 0 {
				v = VerdictPass
			} else if tc.Verdict == 1 {
				v = VerdictFail
			}
			at := stamp(tc.At)
			if kind, due := Due(s, at, tc.FiveMin, true); due {
				s, _ = machineStep(s, kind, v, at)
			}
			if s.Identity != identity(tc.Identity) {
				t.Fatalf("identity %d, want %d (raw=%d)", s.Identity, identity(tc.Identity), tc.Verdict)
			}
		})
	}
}
