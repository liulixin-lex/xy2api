//go:build unit

package schedquality

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestRecoveryTrialTable(t *testing.T) {
	origin := machineNow.Add(5 * time.Second)
	for _, tc := range []struct {
		name    string
		offset  time.Duration
		verdict Verdict
		turns   int
		action  Action
		reason  string
	}{
		{"one second before minimum", TrialMinimum - time.Second, VerdictPass, 40, "", ""},
		{"39 turns at minimum", TrialMinimum, VerdictPass, 39, "", ""},
		{"40 turns at minimum", TrialMinimum, VerdictPass, 40, ActionTrialPass, "good"},
		{"one second before deadline", TrialDuration - time.Second, VerdictUnknown, 0, "", ""},
		{"39 turns at deadline", TrialDuration, VerdictPass, 39, ActionTrialTimeout, "deadline"},
		{"no samples at deadline", TrialDuration, VerdictUnknown, 0, ActionTrialTimeout, "deadline"},
		{"fail at minimum", TrialMinimum, VerdictFail, 40, ActionTrialFail, "fail"},
		{"fail at deadline", TrialDuration, VerdictFail, 40, ActionTrialFail, "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := machineState(DimensionCache)
			s.Trial, s.Epoch, s.TrialUntil = true, origin, origin.Add(TrialDuration)
			at := origin.Add(tc.offset)
			n, a := Step(s, EventTrial, tc.verdict, Basis{Line: s.Line, Cache: &CacheAssessment{Turns: tc.turns}}, at)
			if a != tc.action {
				t.Fatalf("action %s want %s", a, tc.action)
			}
			if a == "" {
				if !n.Trial || n.TrialUntil != s.TrialUntil || n.Epoch != origin {
					t.Fatal("changed trial round")
				}
				return
			}
			if n.Trial || n.Epoch != at || !n.TrialUntil.IsZero() || n.Basis.ReleaseReason != tc.reason || n.Basis.TrialDurationSeconds != int64(tc.offset/time.Second) {
				t.Fatalf("exit %+v basis %+v", n, n.Basis)
			}
			if a == ActionTrialFail && (n.Until != at.Add(RearDuration) || n.Identity != IdentityRear) {
				t.Fatal("failure did not restart rear")
			}
			if a != ActionTrialFail && (n.Identity != IdentityUnknown || !n.Until.IsZero()) {
				t.Fatal("trial did not return to normal")
			}
		})
	}
	s := machineState(DimensionTTFO)
	s.Trial, s.Epoch, s.TrialUntil = true, origin, origin.Add(TrialDuration)
	for _, offset := range []time.Duration{TrialMinimum, TrialDuration} {
		if kind, due := Due(s, origin.Add(offset), false, false); !due || kind != EventTrial {
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
		if minute == 250 && (!n.Trial || n.TrialUntil.Sub(n.EvaluatedAt) != 50*time.Minute) {
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

// Acceptance 3: six orders apply frozen verdicts and preserve mixed exits.
func TestRecoveryIndependentLinesAndMixedBatch(t *testing.T) {
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
			if n.Level != 0 || n.Identity != IdentityRear || n.Until.Sub(machineNow) != RearDuration {
				t.Fatalf("order %v: %+v", order, n)
			}
		}
	}
	inputs[DimensionCache] = Evaluation{Kind: EventTrial, Verdict: VerdictPass, Basis: Basis{Cache: &CacheAssessment{Turns: 40}}}
	delete(inputs, DimensionError)
	after, actions := StepGroup(rows, inputs, machineNow)
	if !reflect.DeepEqual(after[1], rows[1]) {
		t.Fatal("untouched sibling changed")
	}
	if actions[2] != ActionTrialPass || after[2].Trial || after[2].Identity != IdentityUnknown || !after[2].Epoch.Equal(machineNow) {
		t.Fatal("mixed passing trial was swallowed")
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
					input[s.Line] = Evaluation{Kind: kind, Verdict: v, Basis: Basis{Cache: &CacheAssessment{Turns: TrialCacheTurns}}}
				}
			}
			next, actions := StepGroup(rows, input, at)
			rearAfter, restrictedAfter := false, false
			for i, n := range next {
				if actions[i] == ActionEnter || actions[i] == ActionRenew || actions[i] == ActionTrialFail {
					lastBad = at
					if !n.Until.Equal(at.Add(RearDuration)) || !n.Entry.Equal(at) || !n.Epoch.Equal(at) {
						t.Fatalf("zero or wrong rear period: %+v", n)
					}
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
			if count > 13 {
				t.Fatalf("seed %d minute %d: %d entries", seed, minute, count)
			}
			if restricted && restrictedAfter && !lastBad.IsZero() && at.Sub(lastBad) >= RearDuration+TrialDuration {
				t.Fatalf("seed %d: still restricted after %s", seed, at.Sub(lastBad))
			}
			rows = next
		}
	}
}
