//go:build unit

package schedquality

import (
	"testing"
	"time"
)

func screenTurns(slow map[int]bool) []TestTurn {
	return fullTurns(slow, 0)[:TestTurnsPerTier]
}

func TestR2TestCleanAcceptance(t *testing.T) {
	cases := []struct {
		name  string
		turns []TestTurn
		done  bool
		clean bool
	}{
		{"1_screen", screenTurns(nil), true, true},
		{"2_one_slow_warm_turn", screenTurns(map[int]bool{0: true}), true, true},
		{"3_two_slow_warm_turns", screenTurns(map[int]bool{0: true, 1: true}), true, true},
		{"4_cold_timeout", []TestTurn{testTurn(TierSmall, 1, OutcomeTimeout, nil)}, true, false},
		{"5_empty", nil, true, false},
		{"5_not_done", screenTurns(nil), false, false},
		{"5_one_turn", screenTurns(nil)[:1], true, false},
		{"5_two_turns", screenTurns(nil)[:2], true, false},
		{"5_missing_first", screenTurns(nil)[1:], true, false},
		{"5_duplicate", append(screenTurns(nil), testTurn(TierSmall, 3, OutcomeOK, ptr(5000))), true, false},
		{"5_repeated_first_missing_second", []TestTurn{testTurn(TierSmall, 1, OutcomeOK, nil), testTurn(TierSmall, 1, OutcomeOK, nil), testTurn(TierSmall, 3, OutcomeOK, nil)}, true, false},
		{"5_turn_zero", append(screenTurns(nil), testTurn(TierSmall, 0, OutcomeOK, nil)), true, false},
		{"5_turn_four", append(screenTurns(nil), testTurn(TierSmall, 4, OutcomeOK, nil)), true, false},
		{"5_unknown_tier", []TestTurn{testTurn(TestTier(3), 1, OutcomeOK, nil)}, true, false},
		{"6_manual_only_small", screenTurns(nil), true, true},
		{"6_manual_partial_medium", fullTurns(nil, .95)[:4], true, false},
		{"6_manual_or_legacy_full", fullTurns(nil, .95), true, true},
		{"6_full_medium_only", fullTurns(nil, .95)[3:6], true, true},
		{"6_full_tiers_unordered", []TestTurn{testTurn(TierSmall, 3, OutcomeOK, nil), testTurn(TierSmall, 1, OutcomeOK, nil), testTurn(TierSmall, 2, OutcomeOK, nil)}, true, true},
	}
	for _, outcome := range []Outcome{OutcomeTimeout, OutcomeAccountFault, OutcomeOther} {
		turns := screenTurns(nil)
		turns[2].Outcome = outcome
		cases = append(cases, struct {
			name  string
			turns []TestTurn
			done  bool
			clean bool
		}{"5_last_turn_" + outcome.String(), turns, true, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TestClean(testResult(ruleNow, tc.turns, tc.done)); got != tc.clean {
				t.Fatalf("TestClean = %v, want %v", got, tc.clean)
			}
		})
	}
}

func TestR2NextAcceptance(t *testing.T) {
	p := rulePolicy()
	p.TTFTSeconds = 10
	row := cooling(ReasonSpeed, ruleNow.Add(-5*time.Minute), ruleNow, 1)
	now := ruleNow.Add(3 * time.Minute)
	clean := testResult(ruleNow.Add(time.Second), screenTurns(nil), true)
	full := testResult(clean.StartedAt, fullTurns(nil, 0), true)
	oneSlow := testResult(clean.StartedAt, screenTurns(map[int]bool{0: true}), true)
	twoSlow := testResult(clean.StartedAt, screenTurns(map[int]bool{0: true, 1: true}), true)
	timeout := testResult(clean.StartedAt, []TestTurn{testTurn(TierSmall, 1, OutcomeTimeout, nil)}, true)
	partial := testResult(clean.StartedAt, screenTurns(nil)[:2], true)
	remaining := testResult(clean.StartedAt.Add(TestRetryEvery), screenTurns(nil)[2:], true)
	errorFail := Summary{Error: ErrorSummary{N: 40, Sessions: 4, Rate: .5, LCB: .3}}
	errorMarginal := Summary{Error: ErrorSummary{N: 40, Sessions: 4, Rate: .1, LCB: .04}}
	cachePass := Summary{Cache: CacheSummary{Turns: 40, Sessions: 4, Ratio: .95}}
	cacheFail := Summary{Cache: CacheSummary{Turns: 40, Sessions: 4, Ratio: .5}}
	cases := []struct {
		name      string
		prev      *StateRow
		summary   Summary
		tests     []TestResult
		gateOff   bool
		testsOff  bool
		waitMax   bool
		resetAt   time.Time
		state     State
		reason    Reason
		via       Via
		wantTest  bool
		unchanged bool
	}{
		{name: "1_clean_screen_immediate_takeover", tests: []TestResult{clean}, via: ViaScreenClean},
		{name: "2_one_slow_clean_screen", tests: []TestResult{oneSlow}, via: ViaScreenClean},
		{name: "3_two_slow_turns_block_clean", tests: []TestResult{twoSlow}, state: StateCooling, reason: ReasonTestSpeed},
		{name: "4_first_timeout_waits", tests: []TestResult{timeout}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "4_repeated_timeout_pools", tests: []TestResult{timeout, testResult(timeout.StartedAt.Add(TestRetryEvery), timeout.Turns, true)}, state: StateCooling, reason: ReasonTestSpeed},
		{name: "5_incomplete_records_cannot_combine_clean", tests: []TestResult{partial, remaining}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "5_any_clean_record_after_dirty", tests: []TestResult{timeout, testResult(timeout.StartedAt.Add(TestRetryEvery), clean.Turns, true)}, via: ViaScreenClean},
		{name: "5_any_clean_record_before_dirty", tests: []TestResult{clean, testResult(clean.StartedAt.Add(TestRetryEvery), timeout.Turns, true)}, via: ViaScreenClean},
		{name: "6_manual_only_small_is_clean", tests: []TestResult{clean}, testsOff: true, via: ViaScreenClean},
		{name: "6_partial_medium_is_dirty", tests: []TestResult{testResult(clean.StartedAt, full.Turns[:4], true)}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "7_real_speed_pass", summary: realSpeed(VerdictPass, p), tests: []TestResult{full}, testsOff: true, waitMax: true, via: ViaRealPass},
		{name: "7_test_speed_pass", tests: []TestResult{full}, testsOff: true, waitMax: true, via: ViaTestPass},
		{name: "7_real_error_pass", prev: rowWithReason(row, ReasonTestError), summary: Summary{Error: ErrorSummary{N: 40, Sessions: 4}}, tests: []TestResult{full}, via: ViaRealPass},
		{name: "7_test_error_pass", prev: rowWithReason(row, ReasonError), tests: []TestResult{full}, via: ViaTestPass},
		{name: "7_acute_cause_uses_speed_source", prev: rowWithReason(row, ReasonAcuteTimeout), tests: []TestResult{full}, via: ViaTestPass},
		{name: "7_tests_off", testsOff: true, reason: ReasonUnverified, via: ViaTestsOff},
		{name: "7_wait_max", waitMax: true, reason: ReasonUnverified, via: ViaWaitMax},
		{name: "7_tests_off_precedes_wait_max", testsOff: true, waitMax: true, reason: ReasonUnverified, via: ViaTestsOff},
		{name: "7_gate_off_precedes_acute_pass_clean_and_time", summary: Summary{Acute: true}, tests: []TestResult{full}, gateOff: true, testsOff: true, waitMax: true, reason: ReasonUnverified, via: ViaGateOff},
		{name: "7_clean_precedes_unverified", tests: []TestResult{clean}, testsOff: true, waitMax: true, via: ViaScreenClean},
		{name: "8_acute_precedes_test_pass", summary: Summary{Acute: true}, tests: []TestResult{full}, state: StateCooling, reason: ReasonAcuteTimeout},
		{name: "8_speed_fail_precedes_test_pass", summary: realSpeed(VerdictFail, p), tests: []TestResult{full}, state: StateCooling, reason: ReasonSpeed},
		{name: "8_speed_marginal_precedes_clean", summary: realSpeed(VerdictMarginal, p), tests: []TestResult{clean}, state: StateCooling, reason: ReasonSpeed},
		{name: "8_error_fail_precedes_test_pass", summary: errorFail, tests: []TestResult{full}, state: StateCooling, reason: ReasonError},
		{name: "8_error_marginal_precedes_test_pass", summary: errorMarginal, tests: []TestResult{full}, state: StateCooling, reason: ReasonError},
		{name: "8_cache_fail_precedes_test_pass", summary: cacheFail, tests: []TestResult{full}, state: StateCooling, reason: ReasonCache},
		{name: "8_normal_to_cooling_has_no_via", prev: &StateRow{AccountID: row.AccountID, Model: row.Model, GroupID: row.GroupID, Level: 1, Since: ruleNow}, summary: realSpeed(VerdictFail, p), tests: []TestResult{full}, state: StateCooling, reason: ReasonSpeed},
		{name: "9_legacy_cache_fail", prev: rowWithReason(row, ReasonTestCache), summary: cacheFail, tests: []TestResult{clean}, state: StateCooling, reason: ReasonCache},
		{name: "9_legacy_cache_pass", prev: rowWithReason(row, ReasonTestCache), summary: cachePass, tests: []TestResult{clean}, via: ViaRealPass},
		{name: "9_legacy_cache_nodata_clean", prev: rowWithReason(row, ReasonTestCache), tests: []TestResult{clean}, via: ViaScreenClean},
		{name: "9_legacy_cache_nodata_wait", prev: rowWithReason(row, ReasonTestCache), tests: []TestResult{timeout}, state: StateCooling, reason: ReasonTestCache, wantTest: true, unchanged: true},
		{name: "9_legacy_cache_nodata_wait_max", prev: rowWithReason(row, ReasonTestCache), tests: []TestResult{timeout}, waitMax: true, reason: ReasonUnverified, via: ViaWaitMax},
		{name: "9_low_test_cache_does_not_cool_normal", prev: &StateRow{AccountID: row.AccountID, Model: row.Model, GroupID: row.GroupID}, tests: []TestResult{full}, unchanged: true},
		{name: "11_old_test_excluded", tests: []TestResult{testResult(row.Since.Add(-time.Nanosecond), clean.Turns, true)}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "11_since_boundary_excluded", tests: []TestResult{testResult(row.Since, clean.Turns, true)}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "11_reset_boundary_excluded", resetAt: clean.StartedAt, tests: []TestResult{clean}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "11_after_reset_included", resetAt: clean.StartedAt.Add(-time.Nanosecond), tests: []TestResult{clean}, via: ViaScreenClean},
		{name: "11_aborted_excluded", tests: []TestResult{testResult(clean.StartedAt, clean.Turns, false)}, state: StateCooling, reason: ReasonSpeed, wantTest: true, unchanged: true},
		{name: "13_old_level_six_until_preserved", prev: &StateRow{AccountID: row.AccountID, Model: row.Model, GroupID: row.GroupID, State: StateCooling, Reason: ReasonTestCache, Level: 6, Since: row.Since, Until: now.Add(time.Minute)}, summary: Summary{Acute: true}, tests: []TestResult{full}, gateOff: true, testsOff: true, state: StateCooling, reason: ReasonTestCache, unchanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev, at := row, now
			if tc.prev != nil {
				prev = *tc.prev
			}
			if tc.waitMax {
				at = prev.Until.Add(TestWaitMax)
			}
			policy := p
			policy.Enabled = !tc.gateOff
			in := Inputs{Policy: policy, Summary: tc.summary, Tests: tc.tests, TestsEnabled: !tc.testsOff, ResetAt: tc.resetAt}
			next, changed, remove, wantTest, via := Next(prev, in, at)
			if next.State != tc.state || next.Reason != tc.reason || via != tc.via || wantTest != tc.wantTest || remove {
				t.Fatalf("next=%+v changed=%v remove=%v wantTest=%v via=%q", next, changed, remove, wantTest, via)
			}
			if tc.unchanged {
				if changed || next != prev {
					t.Fatalf("row changed: %+v -> %+v", prev, next)
				}
			} else {
				if !changed || next.Since != at {
					t.Fatalf("transition must start a new round: %+v changed=%v", next, changed)
				}
				if next.State == StateNormal && (next.Level != prev.Level || !next.Until.IsZero()) {
					t.Fatalf("recovery must keep level and clear until: %+v", next)
				}
				if next.State == StateCooling && (next.Level != 2 || next.Until.Sub(at) != 15*time.Minute) {
					t.Fatalf("re-cooling must take next ladder step: %+v", next)
				}
			}
			if tc.via == ViaScreenClean && !CanTakeOver(StateView{State: next.State, Level: next.Level, Reason: next.Reason, Since: next.Since}, true, at) {
				t.Fatal("clean screening recovery must allow immediate takeover")
			}
		})
	}
}

func rowWithReason(row StateRow, reason Reason) *StateRow {
	row.Reason = reason
	return &row
}

func TestR2CooldownLadderAcceptance(t *testing.T) {
	p := rulePolicy()
	for _, tc := range []struct {
		name     string
		level    int
		duration time.Duration
		next     int
	}{
		{"10_first", 0, 5 * time.Minute, 1},
		{"10_second", 1, 15 * time.Minute, 2},
		{"10_third", 2, 30 * time.Minute, 3},
		{"10_fourth", 3, time.Hour, 4},
		{"10_fifth", 4, 2 * time.Hour, 5},
		{"10_old_level_five", 5, 2 * time.Hour, 5},
		{"13_old_level_six", 6, 2 * time.Hour, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Hour), ruleNow, tc.level)
			next, changed, remove, wantTest, via := Next(row, Inputs{Policy: p, Summary: realSpeed(VerdictFail, p)}, ruleNow)
			if next.State != StateCooling || next.Level != tc.next || next.Until.Sub(ruleNow) != tc.duration || !changed || remove || wantTest || via != "" {
				t.Fatalf("next=%+v changed=%v remove=%v wantTest=%v via=%q", next, changed, remove, wantTest, via)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		level   int
		reason  Reason
	}{
		{"10_before_decay", LevelDecayAfter - time.Nanosecond, 5, ReasonUnverified},
		{"10_exact_decay", LevelDecayAfter, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := StateRow{State: StateNormal, Level: 5, Reason: ReasonUnverified, Since: ruleNow}
			next, _, remove, wantTest, via := Next(row, Inputs{Policy: p}, ruleNow.Add(tc.elapsed))
			if next.Level != tc.level || next.Reason != tc.reason || remove || wantTest || via != "" {
				t.Fatalf("next=%+v remove=%v wantTest=%v via=%q", next, remove, wantTest, via)
			}
		})
	}
}

func TestR2CurrentTestFreshnessAcceptance(t *testing.T) {
	p := rulePolicy()
	p.TTFTSeconds = 10
	row := cooling(ReasonTestCache, ruleNow.Add(-TestFreshFor-time.Hour), ruleNow, 6)
	for _, tc := range []struct {
		name  string
		start time.Time
		fresh bool
	}{
		{"11_before_fresh_boundary", ruleNow.Add(-TestFreshFor - time.Nanosecond), false},
		{"11_exact_fresh_boundary", ruleNow.Add(-TestFreshFor), true},
		{"13_small_only_new_record", ruleNow.Add(-time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			test := testResult(tc.start, screenTurns(nil), true)
			next, changed, remove, wantTest, via := Next(row, Inputs{Policy: p, Tests: []TestResult{test}, TestsEnabled: true}, ruleNow)
			if remove || changed != tc.fresh || wantTest == tc.fresh {
				t.Fatalf("next=%+v changed=%v remove=%v wantTest=%v via=%q", next, changed, remove, wantTest, via)
			}
			if tc.fresh {
				if next.State != StateNormal || next.Reason != "" || next.Level != 6 || via != ViaScreenClean {
					t.Fatalf("fresh screen must release old row: %+v via=%q", next, via)
				}
			} else if next != row || via != "" {
				t.Fatalf("old screen must leave row untouched: %+v via=%q", next, via)
			}
		})
	}
}

func TestR2TestCacheAcceptance(t *testing.T) {
	p := rulePolicy()
	p.TTFTSeconds = 10
	for _, tc := range []struct {
		name  string
		turns []TestTurn
	}{
		{"9_screen", screenTurns(nil)},
		{"9_full_low_cache", fullTurns(nil, 0)},
		{"9_full_high_cache", fullTurns(nil, 1)},
		{"9_pooled_full_records", append(fullTurns(nil, 0), fullTurns(nil, 0)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JudgeTest(tc.turns, "gpt-6.1-sol", p); got.Cache != VerdictNoData {
				t.Fatalf("test cache must have no verdict: %+v", got)
			}
			tests := []TestResult{testResult(ruleNow.Add(-time.Minute), tc.turns, true)}
			eff, from := Effective(Verdicts{}, tests, "gpt-6.1-sol", p, time.Time{}, time.Time{}, ruleNow)
			if eff.Cache != VerdictNoData || from[2] {
				t.Fatalf("test cache must not fill real nodata: %+v fromTest=%v", eff, from)
			}
		})
	}
}
