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
			AvailableAt: float64(availableNS) / 1e9, StartedAt: float64(availableNS-s.StartedBeforeAvailableNS[i]) / 1e9,
			TTFOMs: s.TTFOMs[i], CensoredMs: s.CensoredMs[i], Stream: s.Stream[i],
			Outcome: s.Outcome[i], SwitchedAccount: s.SwitchedAccount[i],
		}
	}
	return rows
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
	var maxAbs, maxRel float64
	var maxAbsField, maxRelField string
	passed := 0
	coverage := make(map[string]bool)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in := RuleBInput{Scope: tc.Scope, Now: tc.Now, Cut: tc.Cut, LimitSeconds: tc.LimitSeconds,
				Samples: resolve(tc.Samples), GroupSamples: resolve(tc.GroupSamples)}
			for _, p := range tc.Peers {
				in.Peers = append(in.Peers, RuleBPeer{Scope: p.Scope, Identity: p.Identity, Epoch: p.Epoch, Samples: resolve(p.Samples)})
			}
			got, err := EvaluateRuleB(in, tc.Params)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.Expected
			if got.State != want.State || got.Absolute != want.Absolute || got.Relative != want.Relative || got.GoodBase != want.GoodBase {
				t.Errorf("state/gates = %v/%v/%v/%v, want %v/%v/%v/%v", got.State, got.Absolute, got.Relative, got.GoodBase,
					want.State, want.Absolute, want.Relative, want.GoodBase)
			}
			if (got.State == RuleBUnknown) != (len(got.UnknownReasons) > 0) {
				t.Errorf("state %v has reasons %v", got.State, got.UnknownReasons)
			}
			if strings.HasPrefix(tc.Name, "three_states/") && strings.HasSuffix(tc.Name, "/narrow") &&
				!slices.Contains(got.UnknownReasons, RuleBRelativeVeto) {
				t.Errorf("good base with relative confirmation must report relative veto: %v", got.UnknownReasons)
			}
			for _, reason := range got.UnknownReasons {
				coverage[string(reason)] = true
			}
			own, peers, group, local := got.Counts, got.PeerCounts, got.PriorGroupCounts, got.PriorLocalCounts
			for feature, applies := range map[string]bool{
				"good": got.State == RuleBGood, "bad": got.State == RuleBBad,
				"no_peer_evidence":         peers.N-peers.X == 0 && got.U == nil,
				"single_slow_session_veto": got.State == RuleBUnknown && got.Absolute && got.Relative && own.D == 1,
				"unknown_blocks_good": got.State == RuleBUnknown && own.N >= 20-1e-8 && own.S >= 3 && !got.Relative &&
					got.WorstMean > tc.Params.Tolerance && (own.K+5*got.Prior)/(own.N+5) <= tc.Params.Tolerance,
				"prior_fallback":      (group.N-local.N)-(group.X-local.X) <= 0 && got.Prior == .1,
				"prior_clipped":       got.Prior == .001 || got.Prior == .999,
				"peer_unknown_as_bad": peers.X > 0 && got.PeerA != nil,
			} {
				coverage[feature] = coverage[feature] || applies
			}
			if own.N > 0 && own.N < 20 && got.RelativeThreshold != nil {
				unpaddedB := got.B - (20 - own.N)
				if 1-ruleBBetaCDF(got.A, unpaddedB, tc.Params.Tolerance) != got.AbsoluteProbability &&
					1-ruleBBetaCDF(got.A, unpaddedB, *got.RelativeThreshold) != *got.RelativeProbability {
					coverage["padding_changes_both_probabilities"] = true
				}
			}
			for _, sample := range in.Samples {
				if sample.TTFOMs <= 0 && sample.CensoredMs/1000 >= in.LimitSeconds {
					coverage["proven_censor"] = true
				}
			}
			check := func(field string, actual, expected float64) {
				abs := math.Abs(actual - expected)
				rel := 0.0
				if expected != 0 {
					rel = abs / math.Abs(expected)
				}
				if abs > maxAbs {
					maxAbs, maxAbsField = abs, tc.Name+"/"+field
				}
				if rel > maxRel {
					maxRel, maxRelField = rel, tc.Name+"/"+field
				}
				if math.IsNaN(actual) || math.IsInf(actual, 0) || (abs > 1e-8 && (expected == 0 || rel > 1e-9)) {
					t.Errorf("%s = %.17g, want %.17g (abs %.3g, rel %.3g)", field, actual, expected, abs, rel)
				}
			}
			for _, c := range []struct {
				name      string
				got, want RuleBCounts
			}{
				{"own", got.Counts, want.Counts}, {"prior_group", got.PriorGroupCounts, want.PriorGroupCounts},
				{"prior_local", got.PriorLocalCounts, want.PriorLocalCounts}, {"peers", got.PeerCounts, want.PeerCounts},
			} {
				check(c.name+".N", c.got.N, c.want.N)
				check(c.name+".K", c.got.K, c.want.K)
				check(c.name+".X", c.got.X, c.want.X)
				if c.got.S != c.want.S || c.got.D != c.want.D {
					t.Errorf("%s sessions = %d/%d, want %d/%d", c.name, c.got.S, c.got.D, c.want.S, c.want.D)
				}
			}
			for _, v := range []struct {
				name      string
				got, want float64
			}{
				{"prior", got.Prior, want.Prior}, {"peer_prior", got.PeerPrior, want.PeerPrior},
				{"a", got.A, want.A}, {"b", got.B, want.B},
				{"absolute_probability", got.AbsoluteProbability, want.AbsoluteProbability}, {"worst_mean", got.WorstMean, want.WorstMean},
			} {
				check(v.name, v.got, v.want)
			}
			for _, v := range []struct {
				name      string
				got, want *float64
			}{
				{"peer_a", got.PeerA, want.PeerA}, {"peer_b", got.PeerB, want.PeerB}, {"u", got.U, want.U},
				{"relative_threshold", got.RelativeThreshold, want.RelativeThreshold}, {"relative_probability", got.RelativeProbability, want.RelativeProbability},
			} {
				if v.got == nil || v.want == nil {
					if v.got != v.want {
						t.Errorf("%s null mismatch: got %v, want %v", v.name, v.got, v.want)
					}
					continue
				}
				check(v.name, *v.got, *v.want)
			}
			if !t.Failed() {
				passed++
			}
		})
	}
	for _, feature := range []string{
		"good", "bad", "no_peer_evidence", "peer_known_insufficient", "no_samples", "mass_insufficient",
		"sessions_insufficient", "single_slow_session_veto", "absolute_unconfirmed", "relative_unconfirmed",
		"unknown_blocks_good", "prior_fallback", "prior_clipped", "padding_changes_both_probabilities",
		"proven_censor", "peer_unknown_as_bad", "relative_veto",
	} {
		if !coverage[feature] {
			t.Errorf("retained vectors do not cover %s", feature)
		}
	}
	t.Logf("vectors %d/%d; max absolute error %.17g (%s); max relative error %.17g (%s)",
		passed, len(fixture.Cases), maxAbs, maxAbsField, maxRel, maxRelField)
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
		{RuleBParams{.9, .2, .05}, RuleBRelaxed}, {RuleBParams{.8, .3, .05}, RuleBRelaxed}, {RuleBParams{.8, .2, .1}, RuleBRelaxed},
		{RuleBParams{.7, .2, .05}, RuleBTightened}, {RuleBParams{.8, .1, .05}, RuleBTightened}, {RuleBParams{.8, .2, 0}, RuleBTightened},
		{RuleBParams{.9, .1, .05}, RuleBMixed}, {RuleBParams{.7, .2, .1}, RuleBMixed},
	} {
		got, err := CompareRuleBParams(base, tc.next)
		if err != nil || got != tc.want {
			t.Errorf("compare %+v: %v, %v; want %v", tc.next, got, err, tc.want)
		}
	}
}

func TestRuleBEvidenceBoundaries(t *testing.T) {
	base := RuleBSample{AccountID: 1, GroupID: 1, Model: "model", SessionKey: "session",
		StartedAt: 19000, AvailableAt: 20000, TTFOMs: 1000, Stream: true, Outcome: OutcomeOK}
	for _, tc := range []struct {
		name   string
		cut    float64
		mutate func(*RuleBSample)
		want   RuleBCounts
	}{
		{"now-inclusive", 0, func(*RuleBSample) {}, RuleBCounts{N: 1, S: 1}},
		{"not-yet-available", 0, func(s *RuleBSample) { s.AvailableAt = math.Nextafter(20000, math.Inf(1)) }, RuleBCounts{}},
		{"window-inclusive-long-request", math.Inf(-1), func(s *RuleBSample) { s.AvailableAt, s.StartedAt = 5600, -100 }, RuleBCounts{N: 1.0 / 256, S: 1}},
		{"outside-window", 0, func(s *RuleBSample) { s.AvailableAt = math.Nextafter(5600, 0) }, RuleBCounts{}},
		{"half-life-from-availability", 0, func(s *RuleBSample) { s.AvailableAt, s.StartedAt = 18200, 100 }, RuleBCounts{N: .5, S: 1}},
		{"cut-inclusive", 20000, func(s *RuleBSample) { s.StartedAt = 20000 }, RuleBCounts{N: 1, S: 1}},
		{"started-before-cut", 19000, func(s *RuleBSample) { s.StartedAt = math.Nextafter(19000, 0) }, RuleBCounts{}},
		{"available-before-cut", 19000, func(s *RuleBSample) { s.AvailableAt = math.Nextafter(19000, 0) }, RuleBCounts{}},
		{"equal-limit-is-good", 0, func(s *RuleBSample) { s.TTFOMs = 10000 }, RuleBCounts{N: 1, S: 1}},
		{"above-limit-is-slow", 0, func(s *RuleBSample) { s.TTFOMs = 10000.001 }, RuleBCounts{N: 1, K: 1, S: 1, D: 1}},
		{"censor-equal-is-slow", 0, func(s *RuleBSample) { s.TTFOMs, s.CensoredMs, s.Outcome = 0, 10000, OutcomeTimeout }, RuleBCounts{N: 1, K: 1, S: 1, D: 1}},
		{"censor-below-is-unknown", 0, func(s *RuleBSample) { s.TTFOMs, s.CensoredMs, s.Outcome = 0, 9999.999, OutcomeTimeout }, RuleBCounts{N: 1, X: 1, S: 1}},
		{"censor-above-is-slow", 0, func(s *RuleBSample) { s.TTFOMs, s.CensoredMs, s.Outcome = 0, 10000.001, OutcomeTimeout }, RuleBCounts{N: 1, K: 1, S: 1, D: 1}},
		{"known-first-output-wins", 0, func(s *RuleBSample) { s.CensoredMs, s.Outcome = 20000, OutcomeTimeout }, RuleBCounts{N: 1, S: 1}},
		{"missing-session", 0, func(s *RuleBSample) { s.SessionKey, s.TTFOMs = "", 20000 }, RuleBCounts{N: 1, K: 1}},
		{"switch-first-turn", 0, func(s *RuleBSample) { s.SwitchedAccount = true }, RuleBCounts{}},
		{"non-stream", 0, func(s *RuleBSample) { s.Stream = false }, RuleBCounts{}},
		{"no-output-no-timeout", 0, func(s *RuleBSample) { s.TTFOMs = 0 }, RuleBCounts{}},
		{"other-account", 0, func(s *RuleBSample) { s.AccountID = 2 }, RuleBCounts{}},
		{"other-model", 0, func(s *RuleBSample) { s.Model = "other" }, RuleBCounts{}},
		{"other-source-group", 0, func(s *RuleBSample) { s.GroupID = 2 }, RuleBCounts{N: 1, S: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := base
			tc.mutate(&sample)
			got := CountRuleBEvidence([]RuleBSample{sample}, RuleBScope{AccountID: 1, Model: "model"}, 20000, tc.cut, 10)
			if got != tc.want {
				t.Fatalf("counts = %+v, want %+v", got, tc.want)
			}
		})
	}
	base.TTFOMs = 20000
	rows := []RuleBSample{base, base}
	got := CountRuleBEvidence(rows, RuleBScope{AccountID: 1, Model: "model"}, 20000, 0, 10)
	if got != (RuleBCounts{N: 2, K: 2, S: 1, D: 1}) {
		t.Fatal(got)
	}
	rows[1].GroupID = 2
	got = CountRuleBEvidence(rows, RuleBScope{AccountID: 1, Model: "model"}, 20000, 0, 10)
	if got.S != 2 || got.D != 2 {
		t.Fatalf("source group must be part of session key: %+v", got)
	}
}

func ruleBTestInput(ownCount, peerCount int, slow bool) RuleBInput {
	rows := func(account int64, n int, isSlow bool) []RuleBSample {
		out := make([]RuleBSample, n)
		for i := range out {
			out[i] = RuleBSample{AccountID: account, GroupID: 1, Model: "model", SessionKey: fmt.Sprintf("session-%d", i%3),
				StartedAt: 20000, AvailableAt: 20000, TTFOMs: 1000, Stream: true, Outcome: OutcomeOK}
			if isSlow {
				out[i].TTFOMs = 11000
			}
		}
		return out
	}
	in := RuleBInput{Scope: RuleBScope{AccountID: 1, GroupID: 1, Model: "model"}, Now: 20000, Cut: math.Inf(-1), LimitSeconds: 10,
		Samples: rows(1, ownCount, slow),
		Peers:   []RuleBPeer{{Scope: RuleBScope{AccountID: 2, GroupID: 1, Model: "model"}, Identity: -1, Epoch: math.Inf(-1), Samples: rows(2, peerCount, false)}},
	}
	in.GroupSamples = append(slices.Clone(in.Samples), in.Peers[0].Samples...)
	return in
}

func evaluateRuleBTest(t *testing.T, in RuleBInput, p RuleBParams) RuleBResult {
	t.Helper()
	r, err := EvaluateRuleB(in, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRuleBPeerAndUnknownBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*RuleBInput)
		state  RuleBState
		reason RuleBUnknownReason
		uNil   bool
	}{
		{"peer-known-20", func(*RuleBInput) {}, RuleBGood, "", false},
		{"peer-known-19.99", func(in *RuleBInput) {
			in.Peers[0].Samples[0].AvailableAt = in.Now + RuleBHalfLifeSeconds*math.Log2(.99)
		}, RuleBUnknown, RuleBPeerKnownInsufficient, true},
		{"no-members", func(in *RuleBInput) { in.Peers = nil }, RuleBUnknown, RuleBNoPeers, true},
		{"all-rear", func(in *RuleBInput) { in.Peers[0].Identity = 1 }, RuleBUnknown, RuleBAllPeersRear, true},
		{"self-is-not-peer", func(in *RuleBInput) { in.Peers[0].Scope.AccountID = 1 }, RuleBUnknown, RuleBNoPeers, true},
		{"other-group-is-not-peer", func(in *RuleBInput) { in.Peers[0].Scope.GroupID = 2 }, RuleBUnknown, RuleBNoPeers, true},
		{"other-model-is-not-peer", func(in *RuleBInput) { in.Peers[0].Scope.Model = "other" }, RuleBUnknown, RuleBNoPeers, true},
		{"pending-confirmation-is-front", func(in *RuleBInput) { in.Peers[0].Identity = 2 }, RuleBGood, "", false},
		{"peer-epoch-cuts-start", func(in *RuleBInput) { in.Peers[0].Epoch = 20001 }, RuleBUnknown, RuleBPeerKnownInsufficient, true},
		{"empty-own", func(in *RuleBInput) { in.Samples = nil }, RuleBUnknown, RuleBNoSamples, false},
		{"own-mass-19", func(in *RuleBInput) { in.Samples = in.Samples[:19] }, RuleBUnknown, RuleBMassInsufficient, false},
		{"own-good-N-tolerance", func(in *RuleBInput) {
			in.Samples[0].AvailableAt = in.Now + RuleBHalfLifeSeconds*math.Log2(1-5e-9)
		}, RuleBGood, "", false},
		{"one-session", func(in *RuleBInput) {
			for i := range in.Samples {
				in.Samples[i].SessionKey = "one"
			}
		}, RuleBUnknown, RuleBSessionsInsufficient, false},
		{"unknowns-block-good", func(in *RuleBInput) {
			for i := 0; i < 10; i++ {
				in.Samples[i].TTFOMs, in.Samples[i].Outcome = 0, OutcomeTimeout
			}
		}, RuleBUnknown, RuleBWorstMeanAboveTolerance, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ruleBTestInput(20, 20, false)
			tc.mutate(&in)
			got := evaluateRuleBTest(t, in, DefaultRuleBParams())
			if got.State != tc.state || (got.U == nil) != tc.uNil ||
				(tc.reason != "" && !slices.Contains(got.UnknownReasons, tc.reason)) {
				t.Fatalf("result = %+v", got)
			}
			if tc.uNil && (got.RelativeProbability != nil || got.RelativeThreshold != nil || got.PeerA != nil || got.PeerB != nil) {
				t.Fatalf("insufficient peers must leave relative evidence null: %+v", got)
			}
		})
	}
}

func TestRuleBPriorsPosteriorsAndPurity(t *testing.T) {
	in := ruleBTestInput(19, 20, true)
	before := in
	before.Samples, before.GroupSamples, before.Peers = slices.Clone(in.Samples), slices.Clone(in.GroupSamples), slices.Clone(in.Peers)
	for i := range before.Peers {
		before.Peers[i].Samples = slices.Clone(in.Peers[i].Samples)
	}
	r := evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.State != RuleBBad || r.Counts.N != 19 || r.Prior != .001 || math.Abs(r.PeerPrior-19.0/39) > 1e-15 ||
		r.A != 19.005 || r.B != 5.995 || r.WorstMean != 19.005/24 {
		t.Fatalf("low-N bad decision, leave-one-out or padding failed: %+v", r)
	}
	if !reflect.DeepEqual(before, in) || !reflect.DeepEqual(r, evaluateRuleBTest(t, in, DefaultRuleBParams())) {
		t.Fatal("evaluation mutated inputs or was nondeterministic")
	}
	for i := range in.Samples {
		in.Samples[i].SessionKey = "one"
	}
	r = evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.State != RuleBUnknown || !r.Absolute || !r.Relative || !slices.Contains(r.UnknownReasons, RuleBSlowSessionsInsufficient) {
		t.Fatalf("one slow session must veto bad: %+v", r)
	}
	in = ruleBTestInput(20, 20, false)
	in.Cut = 20001
	r = evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.Counts.N != 0 || r.PriorGroupCounts.N != 40 || r.PriorLocalCounts.N != 20 || r.PeerCounts.N != 20 {
		t.Fatalf("own cutoff must not truncate priors or peers: %+v", r)
	}
	in.GroupSamples = nil
	r = evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.Prior != .1 || r.PeerPrior != .1 {
		t.Fatal(r)
	}
	in = ruleBTestInput(20, 30, false)
	for i := 20; i < 30; i++ {
		in.Peers[0].Samples[i].TTFOMs, in.Peers[0].Samples[i].Outcome = 0, OutcomeTimeout
	}
	r = evaluateRuleBTest(t, in, DefaultRuleBParams())
	if r.PeerCounts.X != 10 || r.U == nil || *r.PeerA != 10.005 || *r.PeerB != 24.995 {
		t.Fatalf("peer unknowns must count as bad without filling: %+v", r)
	}
	in = ruleBTestInput(20, 20, true)
	for i := range in.Peers[0].Samples {
		in.Peers[0].Samples[i].TTFOMs = 11000
	}
	in.GroupSamples = append(slices.Clone(in.Samples), in.Peers[0].Samples...)
	p := DefaultRuleBParams()
	p.PeerMargin = .99
	r = evaluateRuleBTest(t, in, p)
	if r.Prior != .999 || r.PeerPrior != .999 || *r.RelativeThreshold != .999999 || *r.RelativeProbability <= 0 {
		t.Fatalf("clipped threshold must still evaluate Beta tail: %+v", r)
	}
}

func TestRuleBBetaBoundaries(t *testing.T) {
	for _, x := range []float64{-1, 0, .001, .2, .5, .8, .999, 1, 2} {
		want := math.Min(1, math.Max(0, x)) // Beta(1,1) is uniform.
		if got := ruleBBetaCDF(1, 1, x); math.Abs(got-want) > 1e-14 {
			t.Fatalf("CDF(%g) = %.17g", x, got)
		}
	}
	if got := ruleBBetaQuantile(1, 1, .5); got != .5-math.Exp2(-25) {
		t.Fatalf("24 bisections with equality moving upper bound: %.17g", got)
	}
}
