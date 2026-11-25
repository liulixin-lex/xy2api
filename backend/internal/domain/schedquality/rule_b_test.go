//go:build unit

package schedquality

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Columns keep the selected reference inputs small without rounding timestamps.
// The converter verifies that nanoseconds round-trip to the original float64s.
type ruleBVectorSamples struct {
	AccountID, GroupID                         []int64
	Model, SessionKey                          []string
	AvailableDeltaNS, StartedBeforeAvailableNS []int64
	TTFOMs, CensoredMs                         []float64
	Stream, SwitchedAccount                    []bool
	Outcome                                    []Outcome
}

func (s ruleBVectorSamples) expand() []RuleBSample {
	rows := make([]RuleBSample, len(s.AccountID))
	var availableNS int64
	for i := range rows {
		availableNS += s.AvailableDeltaNS[i]
		rows[i] = RuleBSample{
			AccountID: s.AccountID[i], GroupID: s.GroupID[i], Model: s.Model[i], SessionKey: s.SessionKey[i],
			AvailableAt: time.Unix(0, availableNS), StartedAt: time.Unix(0, availableNS-s.StartedBeforeAvailableNS[i]),
			TTFOMs: s.TTFOMs[i], CensoredMs: s.CensoredMs[i], Stream: s.Stream[i],
			Outcome: s.Outcome[i], SwitchedAccount: s.SwitchedAccount[i],
		}
	}
	return rows
}

func ruleBFixtureTime(seconds float64) time.Time {
	if seconds == -1e20 { // Reference vectors use this cutoff for all history.
		return time.Time{}
	}
	return time.Unix(0, int64(math.Round(seconds*1e9)))
}

func TestRuleBVectors(t *testing.T) {
	file, err := os.Open("testdata/rule_b_vectors.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var fixture struct {
		Samples ruleBVectorSamples
		Cases   []struct {
			Name                   string
			Params                 RuleBParams
			Scope                  RuleBScope
			Now, Cut, LimitSeconds float64
			Samples, GroupSamples  []int
			Peers                  []struct {
				Scope    RuleBScope
				Identity int
				Epoch    float64
				Samples  []int
			}
			Expected RuleBResult
		}
	}
	if err := json.NewDecoder(reader).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	counts, seen := make(map[string]int), make(map[string]bool)
	for _, tc := range fixture.Cases {
		if seen[tc.Name] {
			t.Fatalf("duplicate vector %s", tc.Name)
		}
		seen[tc.Name] = true
		group, _, _ := strings.Cut(tc.Name, "/")
		counts[group]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"default": 20, "wide": 10, "narrow": 10, "three_states": 6}) {
		t.Fatalf("assessment counts = %v, want 20 + 10 + 10 + 2*3", counts)
	}
	for i := 1; i <= 2; i++ {
		for _, group := range []string{"default", "wide", "narrow"} {
			if name := fmt.Sprintf("three_states/%d/%s", i, group); !seen[name] {
				t.Fatalf("missing fixed-context assessment %s", name)
			}
		}
	}
	samples := fixture.Samples.expand()
	resolve := func(refs []int) []RuleBSample {
		rows := make([]RuleBSample, len(refs))
		for i, ref := range refs {
			rows[i] = samples[ref]
		}
		return rows
	}
	// Retain the frozen reference's own evidence, prior and absolute posterior.
	// Its relative gates and final verdicts describe the retired rule.
	sameFinite := func(got, want float64) bool {
		return !math.IsNaN(got) && !math.IsInf(got, 0) && math.Abs(got-want) <= 1e-8
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in := RuleBInput{Scope: tc.Scope, Now: ruleBFixtureTime(tc.Now), Cut: ruleBFixtureTime(tc.Cut), LimitSeconds: tc.LimitSeconds,
				Samples: resolve(tc.Samples), GroupSamples: resolve(tc.GroupSamples)}
			got := evaluateRuleBTest(t, in, tc.Params)
			want := tc.Expected
			for _, pair := range [][2]RuleBCounts{{got.Counts, want.Counts}, {got.PriorGroupCounts, want.PriorGroupCounts}, {got.PriorLocalCounts, want.PriorLocalCounts}} {
				if !sameFinite(pair[0].N, pair[1].N) || !sameFinite(pair[0].K, pair[1].K) || !sameFinite(pair[0].X, pair[1].X) || pair[0].S != pair[1].S || pair[0].D != pair[1].D {
					t.Fatalf("counts: %+v", pair)
				}
			}
			for _, pair := range [][2]float64{{got.Prior, want.Prior}, {got.A, want.A}, {got.B, want.B}, {got.AbsoluteProbability, want.AbsoluteProbability}, {got.WorstMean, want.WorstMean}} {
				if !sameFinite(pair[0], pair[1]) {
					t.Fatalf("posterior: %v", pair)
				}
			}
			state := RuleBUnknown
			if want.Absolute && want.Counts.D >= 2 {
				state = RuleBBad
			} else if want.GoodBase {
				state = RuleBGood
			}
			if got.State != state || got.Absolute != want.Absolute || got.GoodBase != want.GoodBase {
				t.Fatalf("absolute verdict: %+v", got)
			}
			if (got.State == RuleBUnknown) != (len(got.UnknownReasons) > 0) {
				t.Fatalf("verdict %v and causes %v disagree", got.State, got.UnknownReasons)
			}
		})
	}
}

func TestRuleBParams(t *testing.T) {
	base := DefaultRuleBParams()
	if base != (RuleBParams{Confidence: .8, Tolerance: .2, PeerMargin: .05}) {
		t.Fatal(base)
	}
	for _, field := range []string{"confidence", "tolerance", "peer_margin"} {
		invalid := []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1, 80}
		if field != "peer_margin" {
			invalid = append(invalid, 0)
		}
		if field == "confidence" {
			invalid = append(invalid, .5, .1)
		}
		for _, value := range invalid {
			p := base
			switch field {
			case "confidence":
				p.Confidence = value
			case "tolerance":
				p.Tolerance = value
			case "peer_margin":
				p.PeerMargin = value
			}
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), field) {
				t.Errorf("%s = %v: %v", field, value, err)
			}
			if _, err := EvaluateRuleB(RuleBInput{}, p); err == nil {
				t.Errorf("EvaluateRuleB accepted %s = %v", field, value)
			}
			if _, err := CompareRuleBParams(base, p); err == nil {
				t.Errorf("comparison accepted invalid new %s", field)
			}
			if _, err := CompareRuleBParams(p, base); err == nil {
				t.Errorf("comparison accepted invalid old %s", field)
			}
		}
	}
	for _, p := range []RuleBParams{
		base, {Confidence: .51, Tolerance: .9, PeerMargin: .9},
		{Confidence: math.Nextafter(.5, 1), Tolerance: math.SmallestNonzeroFloat64, PeerMargin: 0},
		{Confidence: math.Nextafter(1, 0), Tolerance: math.Nextafter(1, 0), PeerMargin: math.Nextafter(1, 0)},
	} {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise every component independently and mixed directions atomically.
	for _, tc := range []struct {
		next RuleBParams
		want RuleBParamChange
	}{
		{base, RuleBUnchanged},
		{RuleBParams{.9, .2, .05}, RuleBRelaxed}, {RuleBParams{.8, .3, .05}, RuleBRelaxed}, {RuleBParams{.8, .2, .1}, RuleBUnchanged},
		{RuleBParams{.7, .2, .05}, RuleBTightened}, {RuleBParams{.8, .1, .05}, RuleBTightened}, {RuleBParams{.8, .2, 0}, RuleBUnchanged},
		{RuleBParams{.9, .1, .05}, RuleBMixed}, {RuleBParams{.7, .2, .1}, RuleBTightened},
	} {
		got, err := CompareRuleBParams(base, tc.next)
		if err != nil || got != tc.want {
			t.Errorf("compare %+v: %v, %v; want %v", tc.next, got, err, tc.want)
		}
	}
}

func TestRuleBEvidenceBoundaries(t *testing.T) {
	now := time.Unix(20000, 0)
	epoch := time.Unix(19000, 0)
	noCut := time.Time{}
	base := RuleBSample{AccountID: 1, GroupID: 1, Model: "model", SessionKey: "session",
		StartedAt: epoch, AvailableAt: now, TTFOMs: 1000, Stream: true, Outcome: OutcomeOK}
	for _, tc := range []struct {
		name   string
		cut    time.Time
		mutate func(*RuleBSample)
		want   RuleBCounts
	}{
		{"now-inclusive", noCut, func(*RuleBSample) {}, RuleBCounts{N: 1, S: 1}},
		{"not-yet-available", noCut, func(s *RuleBSample) { s.AvailableAt = now.Add(time.Nanosecond) }, RuleBCounts{}},
		{"window-inclusive-long-request", noCut, func(s *RuleBSample) { s.AvailableAt, s.StartedAt = time.Unix(5600, 0), time.Unix(-100, 0) }, RuleBCounts{N: 1.0 / 256, S: 1}},
		{"outside-window", noCut, func(s *RuleBSample) { s.AvailableAt = time.Unix(5600, -1) }, RuleBCounts{}},
		{"half-life-from-availability", noCut, func(s *RuleBSample) { s.AvailableAt, s.StartedAt = time.Unix(18200, 0), time.Unix(100, 0) }, RuleBCounts{N: .5, S: 1}},
		{"cut-strict", now, func(s *RuleBSample) { s.StartedAt = now }, RuleBCounts{}},
		{"started-before-cut", epoch, func(s *RuleBSample) { s.StartedAt = epoch.Add(-time.Nanosecond) }, RuleBCounts{}},
		{"available-before-cut", epoch, func(s *RuleBSample) { s.AvailableAt = epoch.Add(-time.Nanosecond) }, RuleBCounts{}},
		{"equal-limit-is-good", noCut, func(s *RuleBSample) { s.TTFOMs = 10000 }, RuleBCounts{N: 1, S: 1}},
		{"above-limit-is-slow", noCut, func(s *RuleBSample) { s.TTFOMs = 10000.001 }, RuleBCounts{N: 1, K: 1, S: 1, D: 1}},
		{"censor-equal-is-slow", noCut, func(s *RuleBSample) { s.TTFOMs, s.CensoredMs, s.Outcome = 0, 10000, OutcomeTimeout }, RuleBCounts{N: 1, K: 1, S: 1, D: 1}},
		{"censor-below-is-unknown", noCut, func(s *RuleBSample) { s.TTFOMs, s.CensoredMs, s.Outcome = 0, 9999.999, OutcomeTimeout }, RuleBCounts{N: 1, X: 1, S: 1}},
		{"censor-above-is-slow", noCut, func(s *RuleBSample) { s.TTFOMs, s.CensoredMs, s.Outcome = 0, 10000.001, OutcomeTimeout }, RuleBCounts{N: 1, K: 1, S: 1, D: 1}},
		{"known-first-output-wins", noCut, func(s *RuleBSample) { s.CensoredMs, s.Outcome = 20000, OutcomeTimeout }, RuleBCounts{N: 1, S: 1}},
		{"missing-session", noCut, func(s *RuleBSample) { s.SessionKey, s.TTFOMs = "", 20000 }, RuleBCounts{N: 1, K: 1}},
		{"switch-first-turn", noCut, func(s *RuleBSample) { s.SwitchedAccount = true }, RuleBCounts{}},
		{"non-stream", noCut, func(s *RuleBSample) { s.Stream = false }, RuleBCounts{}},
		{"no-output-no-timeout", noCut, func(s *RuleBSample) { s.TTFOMs = 0 }, RuleBCounts{}},
		{"other-account", noCut, func(s *RuleBSample) { s.AccountID = 2 }, RuleBCounts{}},
		{"other-model", noCut, func(s *RuleBSample) { s.Model = "other" }, RuleBCounts{}},
		{"other-source-group", noCut, func(s *RuleBSample) { s.GroupID = 2 }, RuleBCounts{N: 1, S: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := base
			tc.mutate(&sample)
			got := CountRuleBEvidence([]RuleBSample{sample}, RuleBScope{AccountID: 1, Model: "model"}, now, tc.cut, 10)
			if got != tc.want {
				t.Fatalf("counts = %+v, want %+v", got, tc.want)
			}
		})
	}
	base.TTFOMs = 20000
	rows := []RuleBSample{base, base}
	got := CountRuleBEvidence(rows, RuleBScope{AccountID: 1, Model: "model"}, now, noCut, 10)
	if got != (RuleBCounts{N: 2, K: 2, S: 1, D: 1}) {
		t.Fatal(got)
	}
	rows[1].GroupID = 2
	got = CountRuleBEvidence(rows, RuleBScope{AccountID: 1, Model: "model"}, now, noCut, 10)
	if got.S != 2 || got.D != 2 {
		t.Fatalf("source group must be part of session key: %+v", got)
	}
}

func TestRuleBSwitchedOwnerTimeoutEvidence(t *testing.T) {
	for _, tc := range []struct {
		name               string
		version            int
		warm, full, stream bool
		outcome            Outcome
		want               float64
	}{
		{"v2-false", 2, false, false, true, OutcomeTimeout, 0},
		{"v2-no-proof", 2, true, true, true, OutcomeTimeout, 0},
		{"owner-full-tier", 3, true, true, true, OutcomeTimeout, 1},
		{"short-budget", 3, true, false, true, OutcomeTimeout, 0},
		{"cold-full-tier", 3, false, true, true, OutcomeTimeout, 0},
		{"non-timeout", 3, true, true, true, OutcomeOK, 0},
		{"non-stream", 3, true, true, false, OutcomeTimeout, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ruleBTestInput(1, 1, false)
			for _, rows := range [][]RuleBSample{in.Samples, in.GroupSamples} {
				for i := range rows {
					s := &rows[i]
					s.SwitchedAccount, s.CaptureVersion = true, tc.version
					s.Warm, s.FullTierFirstOutputTimeout = tc.warm, tc.full
					s.Stream, s.Outcome = tc.stream, tc.outcome
					if tc.outcome == OutcomeTimeout {
						s.TTFOMs, s.CensoredMs = 0, 10000
					}
				}
			}
			result := evaluateRuleBTest(t, in, DefaultRuleBParams())
			for _, counts := range []RuleBCounts{result.Counts, result.PriorLocalCounts} {
				if counts.N != tc.want || counts.K != tc.want {
					t.Fatalf("counts = %+v, want N=K=%v", counts, tc.want)
				}
			}
			if c := result.PriorGroupCounts; c.N != 2*tc.want || c.K != 2*tc.want {
				t.Fatalf("group counts = %+v", c)
			}
		})
	}
}

func ruleBTestInput(ownCount, peerCount int, slow bool) RuleBInput {
	rows := func(account int64, n int, isSlow bool) []RuleBSample {
		out := make([]RuleBSample, n)
		for i := range out {
			out[i] = RuleBSample{AccountID: account, GroupID: 1, Model: "model", SessionKey: fmt.Sprintf("session-%d", i%3),
				StartedAt: time.Unix(20000, 0), AvailableAt: time.Unix(20000, 0), TTFOMs: 1000, Stream: true, Outcome: OutcomeOK}
			if isSlow {
				out[i].TTFOMs = 11000
			}
		}
		return out
	}
	in := RuleBInput{Scope: RuleBScope{AccountID: 1, GroupID: 1, Model: "model"}, Now: time.Unix(20000, 0), LimitSeconds: 10,
		Samples: rows(1, ownCount, slow),
	}
	in.GroupSamples = append(slices.Clone(in.Samples), rows(2, peerCount, false)...)
	return in
}

func requireRegisteredRuleBReasons(t *testing.T, reasons []RuleBUnknownReason) {
	t.Helper()
	for _, name := range reasons {
		def := assessmentCauses[assessmentCauseIndex(name)]
		if def.ruleB == nil || def.name != name {
			t.Fatalf("rule B emitted unregistered cause %q", name)
		}
	}
}

func evaluateRuleBTest(t *testing.T, in RuleBInput, p RuleBParams) RuleBResult {
	t.Helper()
	r, err := EvaluateRuleB(in, p)
	if err != nil {
		t.Fatal(err)
	}
	requireRegisteredRuleBReasons(t, r.UnknownReasons)
	return r
}

func TestRuleBAbsoluteAndGoodBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*RuleBInput)
		state  RuleBState
		reason RuleBUnknownReason
	}{
		{"mass-exactly-20", func(*RuleBInput) {}, RuleBGood, ""},
		{"no-peers-bad", func(in *RuleBInput) {
			for i := range in.Samples {
				in.Samples[i].TTFOMs = 11000
			}
		}, RuleBBad, ""},
		{"no-samples", func(in *RuleBInput) { in.Samples = nil }, RuleBUnknown, RuleBNoSamples},
		{"mass-19", func(in *RuleBInput) { in.Samples = in.Samples[:19] }, RuleBUnknown, RuleBMassInsufficient},
		{"mass-13-microseconds-short", func(in *RuleBInput) {
			in.Samples[0].AvailableAt = in.Now.Add(-13 * time.Microsecond)
			in.Samples[0].StartedAt = in.Samples[0].AvailableAt
		}, RuleBUnknown, RuleBMassInsufficient},
		{"mass-below-tolerance", func(in *RuleBInput) {
			in.Samples[0].AvailableAt = in.Now.Add(time.Duration(float64(RuleBHalfLife) * math.Log2(1-2e-8)))
		}, RuleBUnknown, RuleBMassInsufficient},
		{"two-sessions", func(in *RuleBInput) {
			for i := range in.Samples {
				in.Samples[i].SessionKey = fmt.Sprint(i % 2)
			}
		}, RuleBUnknown, RuleBSessionsInsufficient},
		{"one-slow-session", func(in *RuleBInput) {
			for i := range in.Samples {
				in.Samples[i].SessionKey = "one"
				in.Samples[i].TTFOMs = 11000
			}
		}, RuleBUnknown, RuleBSlowSessionsInsufficient},
		{"worst-mean-above", func(in *RuleBInput) {
			for i := 0; i < 5; i++ {
				in.Samples[i].TTFOMs = 0
				in.Samples[i].Outcome = OutcomeTimeout
			}
		}, RuleBUnknown, RuleBWorstMeanAboveTolerance},
		{"worst-mean-below", func(in *RuleBInput) {
			for i := 0; i < 4; i++ {
				in.Samples[i].TTFOMs = 0
				in.Samples[i].Outcome = OutcomeTimeout
			}
		}, RuleBGood, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ruleBTestInput(20, 0, false)
			tc.mutate(&in)
			got := evaluateRuleBTest(t, in, DefaultRuleBParams())
			if got.State != tc.state || (tc.reason != "" && !slices.Contains(got.UnknownReasons, tc.reason)) {
				t.Fatalf("result: %+v", got)
			}
		})
	}
}

func TestRuleBPriorsPosteriorsAndPurity(t *testing.T) {
	in := ruleBTestInput(19, 20, true)
	before := in
	before.Samples = slices.Clone(in.Samples)
	before.GroupSamples = slices.Clone(in.GroupSamples)
	r := evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.State != RuleBBad || r.Counts.N != 19 || r.Prior != .001 || r.A != 19.005 || r.B != 5.995 || r.WorstMean != 19.005/24 {
		t.Fatalf("posterior: %+v", r)
	}
	if !reflect.DeepEqual(before, in) || !reflect.DeepEqual(r, evaluateRuleBTest(t, in, DefaultRuleBParams())) {
		t.Fatal("mutated inputs or nondeterminism")
	}
	in.Cut = in.Now.Add(time.Second)
	r = evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.Counts.N != 0 || r.PriorGroupCounts.N != 39 || r.PriorLocalCounts.N != 19 || r.Prior != .001 {
		t.Fatalf("cut must not change prior: %+v", r)
	}
}

func TestRuleBGroupPriorAndInactiveMargin(t *testing.T) {
	for _, slowOwn := range []int{0, 6, 20} {
		var states []RuleBState
		for _, slowPeers := range []bool{false, true} {
			in := ruleBTestInput(20, 20, false)
			for i := 0; i < slowOwn; i++ {
				in.Samples[i].TTFOMs = 11000
			}
			for i := 20; i < len(in.GroupSamples); i++ {
				if slowPeers {
					in.GroupSamples[i].TTFOMs = 11000
				}
			}
			r := evaluateRuleBTest(t, in, DefaultRuleBParams())
			states = append(states, r.State)
			want := .001
			if slowPeers {
				want = .999
			}
			if r.Prior != want {
				t.Fatalf("prior: %+v", r)
			}
			p := DefaultRuleBParams()
			p.PeerMargin = .99
			if !reflect.DeepEqual(r, evaluateRuleBTest(t, in, p)) {
				t.Fatal("peer_margin changed assessment")
			}
		}
		if slowOwn == 6 && (states[0] != RuleBUnknown || states[1] != RuleBBad) {
			t.Fatalf("prior must change marginal verdict: %v", states)
		}
		if slowOwn == 20 && (states[0] != RuleBBad || states[1] != RuleBBad) {
			t.Fatalf("strong evidence must fail regardless of prior: %v", states)
		}
	}
}

func TestRuleBBetaBoundaries(t *testing.T) {
	for _, x := range []float64{-1, 0, .001, .2, .5, .8, .999, 1, 2} {
		want := math.Min(1, math.Max(0, x)) // Beta(1,1) is uniform.
		if got, err := ruleBBetaCDF(1, 1, x, ruleBBetaMaxIterations); err != nil || math.Abs(got-want) > 1e-14 {
			t.Fatalf("CDF(%g) = %.17g, err = %v", x, got, err)
		}
	}

}

func TestRuleBBetaLargeSymmetricCounts(t *testing.T) {
	for _, a := range []float64{1e3, 1e4, 1e5, 1e6} {
		t.Run(fmt.Sprintf("%g", a), func(t *testing.T) {
			got, err := ruleBBetaCDF(a, a, .5, ruleBBetaMaxIterations)
			if err != nil || math.IsNaN(got) || math.Abs(got-.5) > 1e-8 {
				t.Fatalf("CDF(%g, %g, 0.5) = %.17g, err = %v", a, a, got, err)
			}
		})
	}
}

func TestRuleBLargeCountsDoNotFalselyFail(t *testing.T) {
	in := ruleBTestInput(400000, 42, false)
	for i := range in.Samples {
		in.Samples[i].GroupID = 2
		if i < 200000 {
			in.Samples[i].TTFOMs = 11000
		}
	}
	// Equal fast/slow target-group evidence supplies a symmetric prior.
	in.GroupSamples = ruleBTestInput(0, 2, false).GroupSamples
	in.GroupSamples[0].TTFOMs = 11000
	p := RuleBParams{Confidence: .5000001, Tolerance: math.Nextafter(.5, 0), PeerMargin: 0}
	got := evaluateRuleBTest(t, in, p)
	if got.Counts != (RuleBCounts{N: 400000, K: 200000, S: 3, D: 2}) ||
		got.Prior != .5 || got.A != 200002.5 || got.B != 200002.5 {
		t.Fatalf("unexpected reproduction evidence: %+v", got)
	}
	if got.State != RuleBUnknown || got.Absolute ||
		!slices.Contains(got.UnknownReasons, RuleBAbsoluteUnconfirmed) {
		t.Fatalf("large symmetric posterior must remain unknown: %+v", got)
	}
	t.Logf("state=%d p_abs=%.17g", got.State, got.AbsoluteProbability)
}

func TestRuleBBetaNonConvergence(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func() (float64, error)
	}{
		{"fraction", func() (float64, error) { return ruleBBetaFraction(1000, 1000, .5, 1) }},
		{"cdf-lower", func() (float64, error) { return ruleBBetaCDF(1000, 1000, .49, 1) }},
		{"cdf-upper", func() (float64, error) { return ruleBBetaCDF(1000, 1000, .5, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.call(); err == nil {
				t.Fatal("unconverged fraction must return an error")
			}
		})
	}
	in := ruleBTestInput(20, 20, false)
	if _, err := EvaluateRuleB(in, DefaultRuleBParams()); err != nil {
		t.Fatal(err)
	}
	got, err := evaluateRuleB(in, DefaultRuleBParams(), 1)
	if err == nil || !strings.Contains(err.Error(), "absolute CDF") || got.State != RuleBUnknown {
		t.Fatalf("unconverged: %+v / %v", got, err)
	}
}

func TestObserveRuleBLocalDiscountedWeight(t *testing.T) {
	local := RuleBSample{AccountID: 2, GroupID: 7, Model: "fixture", Stream: true, Outcome: OutcomeOK, TTFOMs: 100,
		StartedAt: machineNow.Add(-RuleBHalfLife), AvailableAt: machineNow.Add(-RuleBHalfLife)}
	borrowed := local
	borrowed.GroupID, borrowed.StartedAt, borrowed.AvailableAt = 9, machineNow, machineNow
	censored := local
	censored.Outcome, censored.TTFOMs = OutcomeTimeout, 0
	censored.StartedAt, censored.AvailableAt = machineNow.Add(-2*RuleBHalfLife), machineNow.Add(-2*RuleBHalfLife)
	excluded := local
	excluded.SwitchedAccount = true
	result, err := EvaluateRuleB(RuleBInput{Scope: RuleBScope{AccountID: 2, GroupID: 7, Model: "fixture"}, Now: machineNow, LimitSeconds: 1,
		Samples: []RuleBSample{local, borrowed, censored, excluded}}, DefaultRuleBParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.SamplesLocal != 2 || result.SamplesTotal != 3 || result.NLocal != .75 || result.Counts.N != 1.75 || result.Counts.X != .25 {
		t.Fatalf("local/total %d/%d, n_local/n/x %v/%v/%v", result.SamplesLocal, result.SamplesTotal, result.NLocal, result.Counts.N, result.Counts.X)
	}
}

func TestRuleBGoodWorstMeanBoundary(t *testing.T) {
	in := ruleBTestInput(20, 0, false)
	for i := 0; i < 4; i++ {
		in.Samples[i].TTFOMs = 0
		in.Samples[i].Outcome = OutcomeTimeout
	}
	for _, tc := range []struct {
		tolerance float64
		want      RuleBState
	}{{.18, RuleBGood}, {math.Nextafter(.18, 0), RuleBUnknown}} {
		p := DefaultRuleBParams()
		p.Tolerance = tc.tolerance
		r := evaluateRuleBTest(t, in, p)
		if r.WorstMean != .18 || r.State != tc.want {
			t.Fatalf("boundary: %+v", r)
		}
	}
}
