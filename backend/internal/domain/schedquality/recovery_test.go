//go:build unit

package schedquality

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// Acceptance 1/5/9: ordered trial exits, equality boundaries and release reasons.
func TestRecoveryTrialTable(t *testing.T) {
	origin := machineNow.Add(5 * time.Second)
	for _, minute := range []int{0, 14, 15, 59, 60, 119, 120} {
		for _, v := range []Verdict{VerdictFail, VerdictPass, VerdictUnknown} {
			for _, own := range []bool{false, true} {
				t.Run(fmt.Sprintf("minute%d/%s/own%t", minute, v, own), func(t *testing.T) {
					s := machineState(DimensionCache)
					s.Trial = true
					s.Epoch = origin
					s.TrialUntil = origin.Add(TrialDuration)
					s.Admissions = 1
					at := origin.Add(time.Duration(minute) * time.Minute)
					n, a := Step(s, EventTrial, v, Basis{OwnSampleAt60: &own}, at)
					want := Action("")
					reason := ""
					switch {
					case v == VerdictFail:
						want = ActionTrialFail
					case v == VerdictPass && minute >= 15:
						want = ActionTrialPass
						reason = "qualified"
					case minute >= 120:
						want = ActionTrialTimeout
						reason = "deadline"
					case minute >= 60 && !own:
						want = ActionTrialTimeout
						reason = "no_samples"
					}
					if a != want {
						t.Fatalf("action %s want %s", a, want)
					}
					if reason != "" {
						if n.Trial || n.Identity != IdentityUnknown || !n.Epoch.Equal(at) || !n.TrialUntil.IsZero() || n.Basis.ReleaseReason != reason || n.Basis.TrialDurationSeconds != int64(minute*60) {
							t.Fatalf("release %+v basis %+v", n, n.Basis)
						}
						if v == VerdictPass && n.LastAssessment[len(n.LastAssessment)-1].Reason != ReasonWithin {
							t.Fatal("lost passing assessment")
						}
					} else if want == "" && (!n.Trial || !n.TrialUntil.Equal(s.TrialUntil)) {
						t.Fatal("changed trial round")
					}
				})
			}
		}
	}
	s := machineState(DimensionTTFO)
	s.Trial = true
	s.Epoch = origin
	s.TrialUntil = origin.Add(time.Hour)
	n, a := Step(s, EventTrial, VerdictUnknown, Basis{OwnSampleAt60: new(true)}, s.TrialUntil)
	if a != ActionTrialTimeout || n.Basis.ReleaseReason != "deadline" || n.Basis.TrialDurationSeconds != 3600 {
		t.Fatal("legacy deadline not retained")
	}
	for _, offset := range []time.Duration{TrialMinimum, TrialSampleCheck} {
		kind, due := Due(s, origin.Add(offset), false, false)
		if !due || kind != EventTrial {
			t.Fatal("non-minute checkpoint missed")
		}
	}
}

// Acceptance 2: missing deadlines cannot strand either position.
func TestRecoveryMissingDeadlinesAndDelayedReview(t *testing.T) {
	for _, trial := range []bool{false, true} {
		s := machineState(DimensionTTFO)
		s.Trial = trial
		s.Epoch = machineNow.Add(-time.Hour)
		if !trial {
			s.Identity = IdentityRear
		}
		kind, due := Due(s, machineNow, false, false)
		if !due {
			t.Fatal("missing deadline not due")
		}
		n, a := Step(s, kind, VerdictUnknown, Basis{}, machineNow)
		if !n.Basis.MissingDeadline || a == "" || trial && n.Trial || !trial && !n.Trial {
			t.Fatalf("missing deadline: %+v/%s", n, a)
		}
	}
	for _, minute := range []int{250, 500} {
		s := machineState(DimensionTTFO)
		s.Identity = IdentityRear
		s.Until = machineNow.Add(240 * time.Minute)
		n, _ := Step(s, EventReview, VerdictUnknown, Basis{}, machineNow.Add(time.Duration(minute)*time.Minute))
		if minute == 250 && (!n.Trial || n.TrialUntil.Sub(n.EvaluatedAt) != 110*time.Minute) {
			t.Fatal("reopened clock")
		}
		if minute == 500 && (n.Trial || n.Identity != IdentityUnknown) {
			t.Fatal("missed hard deadline")
		}
	}
}

func recoveryGroup() []LineState {
	var rows []LineState
	for _, line := range Lines {
		rows = append(rows, machineState(line))
	}
	return rows
}

// Acceptance 3: six orders share frozen history and preserve mixed exits.
func TestRecoverySharedLevelAndMixedBatch(t *testing.T) {
	rows := recoveryGroup()
	rows[0].Identity = IdentityRear
	rows[0].Until = machineNow
	rows[0].Level = 1
	rows[0].Admissions = 1
	rows[1].PendingAt = machineNow.Add(-RegularInterval)
	rows[2].Trial = true
	rows[2].Epoch = machineNow.Add(-time.Hour)
	rows[2].TrialUntil = machineNow.Add(time.Hour)
	inputs := map[Dimension]Evaluation{DimensionTTFO: {Kind: EventReview, Verdict: VerdictFail}, DimensionError: {Kind: EventRegular, Verdict: VerdictFail}, DimensionCache: {Kind: EventTrial, Verdict: VerdictFail}}
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		before := []LineState{rows[order[0]], rows[order[1]], rows[order[2]]}
		after, _ := StepGroup(before, inputs, machineNow)
		for _, n := range after {
			if n.Level != 2 || n.Identity != IdentityRear || n.Until.Sub(machineNow) != 80*time.Minute {
				t.Fatalf("order %v: %+v", order, n)
			}
		}
	}
	inputs[DimensionCache] = Evaluation{Kind: EventTrial, Verdict: VerdictPass}
	delete(inputs, DimensionError)
	after, actions := StepGroup(rows, inputs, machineNow)
	if !reflect.DeepEqual(after[1], rows[1]) {
		t.Fatal("untouched sibling changed")
	}
	if actions[2] != ActionTrialPass || after[2].Trial || after[2].Identity != IdentityUnknown || !after[2].Epoch.Equal(machineNow) {
		t.Fatal("mixed passing trial was swallowed")
	}
	rows = recoveryGroup()
	rows[1].Admissions = 1
	after, _ = StepGroup(rows, map[Dimension]Evaluation{DimensionTTFO: {Verdict: VerdictFail}}, machineNow)
	if after[0].Level != 1 {
		t.Fatal("first admission history ignored")
	}
	rows[1].Level = 2
	after, _ = StepGroup(rows, map[Dimension]Evaluation{DimensionTTFO: {Verdict: VerdictFail}}, machineNow)
	if after[0].Level != 3 {
		t.Fatal("relaxed line ignored sibling history")
	}
}

// Acceptance 4: whole-group clearing and simultaneous new failure precedence.
func TestRecoveryQuietClear(t *testing.T) {
	rows := recoveryGroup()
	for i := range rows {
		rows[i].Level = 2
		rows[i].Admissions = 1
		rows[i].LastRear = machineNow
	}
	at := machineNow.Add(LevelClearQuiet)
	rows[2].Trial = true
	rows[2].Epoch = at.Add(-time.Minute)
	rows[2].TrialUntil = at.Add(time.Hour)
	after, _ := StepGroup(rows, nil, at)
	if after[0].Admissions != 1 || !QuietDeadline(rows).IsZero() {
		t.Fatal("partial normal cleared")
	}
	rows[2].Trial = false
	after, actions := StepGroup(rows, nil, at.Add(-time.Nanosecond))
	if after[0].Admissions != 1 || actions[0] != "" {
		t.Fatal("early clear")
	}
	after, actions = StepGroup(rows, nil, at)
	for i, n := range after {
		if n.Admissions != 0 || n.Level != 0 || actions[i] != ActionClear {
			t.Fatal("group did not clear")
		}
	}
	after, _ = StepGroup(after, map[Dimension]Evaluation{DimensionTTFO: {Verdict: VerdictFail}}, at.Add(RegularInterval))
	if after[0].Level != 0 {
		t.Fatal("later failure ignored earlier clear")
	}
	after, _ = StepGroup(rows, map[Dimension]Evaluation{DimensionTTFO: {Verdict: VerdictFail}}, at)
	if after[0].Level != 3 || after[1].Admissions != 1 {
		t.Fatal("same-time failure lost history")
	}
}

// Acceptance 8: deterministic random three-line sequences over three days.
func TestRecoveryThreeDayBounds(t *testing.T) {
	for seed := int64(0); seed < 24; seed++ {
		rng := rand.New(rand.NewSource(seed))
		rows := recoveryGroup()
		var entries []time.Time
		var lastBad time.Time
		for minute := 0; minute <= 3*24*60; minute++ {
			at := machineNow.Add(time.Duration(minute) * time.Minute)
			rearBefore, restricted := false, false
			input := make(map[Dimension]Evaluation)
			for _, s := range rows {
				rearBefore = rearBefore || s.Identity == IdentityRear
				restricted = restricted || s.Identity == IdentityRear || s.Trial
				if kind, due := Due(s, at, minute%5 == 0, true); due {
					v := []Verdict{VerdictUnknown, VerdictPass, VerdictFail}[rng.Intn(3)]
					if minute%720 >= 300 {
						v = VerdictUnknown
					}
					input[s.Line] = Evaluation{Kind: kind, Verdict: v, Basis: Basis{OwnSampleAt60: new(true)}}
				}
			}
			next, actions := StepGroup(rows, input, at)
			rearAfter, restrictedAfter := false, false
			for i, n := range next {
				if actions[i] == ActionEnter || actions[i] == ActionRenew || actions[i] == ActionTrialFail {
					lastBad = at
				}
				rearAfter = rearAfter || n.Identity == IdentityRear
				restrictedAfter = restrictedAfter || n.Identity == IdentityRear || n.Trial
			}
			if !rearBefore && rearAfter {
				entries = append(entries, at)
			}
			count := 0
			for _, e := range entries {
				if !e.Before(at.Add(-12 * time.Hour)) {
					count++
				}
			}
			if count > 6 {
				t.Fatalf("seed %d minute %d: %d entries", seed, minute, count)
			}
			if restricted && restrictedAfter && !lastBad.IsZero() && at.Sub(lastBad) >= 360*time.Minute {
				t.Fatalf("seed %d: still restricted after %s", seed, at.Sub(lastBad))
			}
			rows = next
		}
	}
}
