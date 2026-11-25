//go:build unit

package schedquality

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAssessmentTTFTCauses(t *testing.T) {
	cases := []struct {
		cause  RuleBUnknownReason
		reason AssessmentReason
		have   float64
		need   int
	}{
		{RuleBNoPeers, ReasonOther, 0, 0},
		{RuleBAllPeersRear, ReasonOther, 0, 0},
		{RuleBPeerKnownInsufficient, ReasonOther, 0, 0},
		{RuleBNoSamples, ReasonOwnShort, 0, 20},
		{RuleBMassInsufficient, ReasonOwnShort, 8.75, 20},
		{RuleBSessionsInsufficient, ReasonOwnShort, 2, 3},
		{RuleBSlowSessionsInsufficient, ReasonOwnShort, 1, 2},
		{RuleBAbsoluteUnconfirmed, ReasonWithin, 0, 0},
		{RuleBRelativeUnconfirmed, ReasonOther, 0, 0},
		{RuleBWorstMeanAboveTolerance, ReasonWithin, 0, 0},
		{RuleBRelativeVeto, ReasonOther, 0, 0},
	}
	var tested []string
	for _, tc := range cases {
		tested = append(tested, string(tc.cause))
		t.Run(string(tc.cause), func(t *testing.T) {
			e := &TTFTEvidence{N: 8.75, S: 2, D: 1, Causes: []RuleBUnknownReason{tc.cause}}
			b := Basis{Line: DimensionTTFO, TTFT: e}
			if tc.cause == RuleBNoSamples {
				e.N = 0
			}
			seg := RecordAssessment(LineState{}, machineNow, EventRegular, VerdictUnknown, "", b)[0]
			require.Equal(t, tc.reason, seg.Reason)
			require.Equal(t, []string{string(tc.cause)}, seg.CauseNames())
			if tc.need > 0 {
				require.Equal(t, new(tc.have), seg.Have)
				require.Equal(t, new(tc.need), seg.Need)
			} else {
				require.Nil(t, seg.Have)
				require.Nil(t, seg.Need)
			}
		})
	}
	// Type-check the whole production package so constants in another file,
	// including unexported, inferred and converted constants, are also covered.
	src, err := build.Default.ImportDir(".", 0)
	require.NoError(t, err)
	fs := token.NewFileSet()
	var files []*ast.File
	for _, name := range src.GoFiles {
		file, err := parser.ParseFile(fs, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		files = append(files, file)
	}
	conf := types.Config{IgnoreFuncBodies: true, Importer: importer.Default()}
	pkg, err := conf.Check("schedquality", fs, files, nil)
	require.NoError(t, err)
	scope := pkg.Scope()
	enum := scope.Lookup("RuleBUnknownReason").Type()
	var declared []string
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if ok && types.Identical(c.Type(), enum) {
			declared = append(declared, constant.StringVal(c.Val()))
		}
	}
	require.ElementsMatch(t, declared, tested)
	require.LessOrEqual(t, len(assessmentCauses), 32)
	var registered []string
	for _, def := range assessmentCauses {
		if def.line == DimensionTTFO {
			registered = append(registered, string(def.name))
		}
	}
	require.ElementsMatch(t, registered, tested)
}

func TestAssessmentRuleBOutputWithoutPeers(t *testing.T) {
	in := ruleBTestInput(40, 0, false)
	result := evaluateRuleBTest(t, in, DefaultRuleBParams())
	require.Equal(t, RuleBGood, result.State)
	require.Empty(t, result.UnknownReasons)
	b := Basis{Line: DimensionTTFO, TTFT: NewTTFTEvidence(result, in.LimitSeconds)}
	seg := RecordAssessment(LineState{}, in.Now, EventRegular, VerdictPass, "", b)[0]
	require.Equal(t, ReasonWithin, seg.Reason)
	require.Empty(t, seg.CauseNames())
	require.Nil(t, seg.Need)
}

func TestAssessmentSlowSessionsRequireConfirmedLines(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		slow, censored, peerSlow int
		absolute                 bool
		reasons                  []RuleBUnknownReason
		primary                  AssessmentReason
	}{
		{"censored worst mean", 0, 16, 0, false, []RuleBUnknownReason{RuleBSlowSessionsInsufficient, RuleBAbsoluteUnconfirmed, RuleBWorstMeanAboveTolerance}, ReasonWithin},
		{"slow peers", 22, 0, 30, true, []RuleBUnknownReason{RuleBSlowSessionsInsufficient, RuleBWorstMeanAboveTolerance}, ReasonOwnShort},
		{"fast peers", 22, 0, 0, true, []RuleBUnknownReason{RuleBSlowSessionsInsufficient, RuleBWorstMeanAboveTolerance}, ReasonOwnShort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := RuleBInput{Scope: RuleBScope{GroupID: 1, AccountID: 1, Model: "m"}, Now: machineNow, LimitSeconds: 15}
			for account := int64(1); account <= 4; account++ {
				var samples []RuleBSample
				for i := 0; i < 40; i++ {
					at := in.Now.Add(-time.Duration(i) * time.Second)
					sample := RuleBSample{AccountID: account, GroupID: 1, Model: "m", SessionKey: fmt.Sprintf("s%d", i/8), StartedAt: at.Add(-time.Second), AvailableAt: at, TTFOMs: 2000, Stream: true, Outcome: OutcomeOK}
					if account == 1 && i < tc.slow {
						sample.TTFOMs, sample.SessionKey = 30000, "s0"
					} else if account != 1 && i < tc.peerSlow {
						sample.TTFOMs = 30000
					}
					samples = append(samples, sample)
				}
				if account == 1 {
					for i := 0; i < tc.censored; i++ {
						at := in.Now.Add(-time.Duration(100+i) * time.Second)
						samples = append(samples, RuleBSample{AccountID: account, GroupID: 1, Model: "m", SessionKey: fmt.Sprintf("t%d", i), StartedAt: at.Add(-5 * time.Second), AvailableAt: at, CensoredMs: 5000, Stream: true, Outcome: OutcomeTimeout})
					}
					in.Samples = samples
				}
				in.GroupSamples = append(in.GroupSamples, samples...)
			}
			result := evaluateRuleBTest(t, in, DefaultRuleBParams())
			require.Equal(t, RuleBUnknown, result.State)
			require.Equal(t, tc.absolute, result.Absolute)
			require.ElementsMatch(t, tc.reasons, result.UnknownReasons)
			b := Basis{Line: DimensionTTFO, TTFT: NewTTFTEvidence(result, in.LimitSeconds)}
			seg := RecordAssessment(LineState{}, in.Now, EventRegular, VerdictUnknown, "", b)[0]
			var causes []string
			for _, cause := range tc.reasons {
				causes = append(causes, string(cause))
			}
			require.ElementsMatch(t, causes, seg.CauseNames())
			require.Equal(t, tc.primary, seg.Reason)
			if tc.primary == ReasonOwnShort {
				require.Equal(t, new(1.0), seg.Have)
				require.Equal(t, new(2), seg.Need)
			} else {
				require.Nil(t, seg.Have)
				require.Nil(t, seg.Need)
			}
		})
	}
}

func TestAssessmentLineOutcomes(t *testing.T) {
	cache := func(c CacheAssessment) Basis { return Basis{Line: DimensionCache, Cache: &c} }
	errLine := func(e ErrorAssessment) Basis { return Basis{Line: DimensionError, Error: &e} }
	recovery := errLine(ErrorAssessment{Clear: 60, RecoveryOverLimit: true})
	ttft := Basis{Line: DimensionTTFO, TTFT: &TTFTEvidence{S: 2,
		Causes: []RuleBUnknownReason{RuleBRelativeVeto, RuleBSessionsInsufficient, RuleBPeerKnownInsufficient},
	}}
	for _, tc := range []struct {
		name    string
		basis   Basis
		kind    EventKind
		verdict Verdict
		action  Action
		reason  AssessmentReason
		causes  []string
		have    float64
		need    int
	}{
		{"cache-turns", cache(CacheAssessment{Turns: 12, MinRatio: .8, Ratio: .79, LowSessions: 1, LowSessions10: new(0)}), EventRegular, VerdictUnknown, "", ReasonOwnShort, []string{cacheTurnsShort, cacheLowSessionsShort}, 12, 20},
		{"cache-low-sessions", cache(CacheAssessment{Turns: 22, MinRatio: .8, LowSessions: 1, LowSessions10: new(0)}), EventRegular, VerdictUnknown, "", ReasonOwnShort, []string{cacheLowSessionsShort}, 0, 1},
		{"clear", errLine(ErrorAssessment{Clear: 13, LowerPass: .1, Limit: .05}), EventRegular, VerdictUnknown, "", ReasonOwnShort, []string{errorClearShort, errorBoundsUnconfirmed}, 13, 50},
		{"bounds", errLine(ErrorAssessment{Clear: 50, LowerPass: .07, Limit: .05}), EventRegular, VerdictUnknown, "", ReasonWithin, []string{errorBoundsUnconfirmed}, 0, 0},
		{"recovery-blocked", recovery, EventReview, VerdictUnknown, ActionTrial, ReasonPending, []string{errorRecoveryOverLimit}, 0, 0},
		{"recovery-open", recovery, EventTrial, VerdictUnknown, "", ReasonTrialOpen, []string{errorRecoveryOverLimit}, 0, 0},
		{"recovery-timeout", recovery, EventTrial, VerdictUnknown, ActionTrialTimeout, ReasonPending, []string{errorRecoveryOverLimit}, 0, 0},
		{"retired-peer-gates", ttft, EventReview, VerdictUnknown, ActionTrial, ReasonOwnShort, []string{string(RuleBPeerKnownInsufficient), string(RuleBSessionsInsufficient), string(RuleBRelativeVeto)}, 2, 3},
		{"trial-open", ttft, EventTrial, VerdictUnknown, "", ReasonTrialOpen, []string{string(RuleBPeerKnownInsufficient), string(RuleBSessionsInsufficient), string(RuleBRelativeVeto)}, 2, 3},
		{"pending-error", errLine(ErrorAssessment{}), EventRegular, VerdictFail, "", ReasonPending, []string{}, 0, 0},
		{"pending-cache", cache(CacheAssessment{}), EventRegular, VerdictFail, "", ReasonPending, []string{}, 0, 0},
		{"enter", cache(CacheAssessment{}), EventRegular, VerdictFail, ActionEnter, ReasonOver, []string{}, 0, 0},
		{"renew", ttft, EventReview, VerdictFail, ActionRenew, ReasonOver, []string{}, 0, 0},
		{"trial-fail", ttft, EventTrial, VerdictFail, ActionTrialFail, ReasonOver, []string{}, 0, 0},
		{"release", ttft, EventReview, VerdictPass, ActionRelease, ReasonWithin, []string{}, 0, 0},
		{"trial-pass", ttft, EventTrial, VerdictPass, ActionTrialPass, ReasonWithin, []string{}, 0, 0},
		{"rule-error", Basis{Line: DimensionTTFO}, EventRegular, VerdictUnknown, "", ReasonOther, []string{}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := RecordAssessment(LineState{}, machineNow, tc.kind, tc.verdict, tc.action, tc.basis)[0]
			require.Equal(t, tc.reason, s.Reason)
			require.Equal(t, tc.causes, s.CauseNames())
			if tc.need != 0 {
				require.Equal(t, new(tc.have), s.Have)
				require.Equal(t, new(tc.need), s.Need)
			} else {
				require.Nil(t, s.Have)
				require.Nil(t, s.Need)
			}
		})
	}
}

func TestAssessmentRuns(t *testing.T) {
	var state LineState
	for i, tc := range []struct {
		clear    int
		verdict  Verdict
		action   Action
		reason   AssessmentReason
		segments int
		count    int64
	}{
		{1, VerdictUnknown, "", ReasonOwnShort, 1, 1},
		{8, VerdictUnknown, "", ReasonOwnShort, 1, 2},
		{52, VerdictPass, "", ReasonWithin, 2, 1},
		{52, VerdictPass, ActionClear, ReasonWithin, 3, 1},
		{52, VerdictPass, "", ReasonWithin, 4, 1},
	} {
		at := machineNow.Add(time.Duration(i) * RegularInterval)
		h := RecordAssessment(state, at, EventRegular, tc.verdict, tc.action, Basis{Error: &ErrorAssessment{Clear: tc.clear}})
		require.Len(t, h, tc.segments)
		last := h[len(h)-1]
		require.Equal(t, at, last.LastAt)
		require.Equal(t, tc.count, last.Count)
		require.Equal(t, tc.reason, last.Reason)
		require.Equal(t, tc.action, last.Action)
		if i == 1 {
			require.Equal(t, machineNow, last.FirstAt)
			require.Equal(t, new(8.0), last.Have)
			require.EqualValues(t, 1, state.LastAssessment[0].Count)
		}
		state.EvaluatedAt, state.LastAssessment = at, h
	}
	require.Equal(t, state.LastAssessment, RecordAssessment(state, state.EvaluatedAt, EventRegular, VerdictUnknown, "", Basis{}))
	state.EvaluatedAt = state.EvaluatedAt.Add(time.Minute) // A legacy writer advanced the watermark.
	h := RecordAssessment(state, state.EvaluatedAt.Add(time.Minute), EventRegular, VerdictPass, "", Basis{})
	require.Len(t, h, 5)
	require.EqualValues(t, 1, h[4].Count)
}

func TestAssessmentEightSegmentSize(t *testing.T) {
	var state LineState
	for i := 0; i < 9; i++ {
		kind := EventRegular
		if i%2 == 1 {
			kind = EventReview
		}
		at := machineNow.Add(time.Duration(i) * time.Minute)
		state.LastAssessment = RecordAssessment(state, at, kind, VerdictUnknown, "", Basis{Error: &ErrorAssessment{Clear: 17}})
		state.EvaluatedAt = at
	}
	require.Len(t, state.LastAssessment, 8)
	for i, s := range state.LastAssessment {
		require.Equal(t, machineNow.Add(time.Duration(i+1)*time.Minute), s.FirstAt)
	}
	typical, err := json.Marshal(state.LastAssessment[:1])
	require.NoError(t, err)
	// Bound every combination of enum fields, with all optional fields present.
	// Any mixed eight-segment history is at most eight copies of the widest
	// segment. 17 significant digits at 10^-6 maximize a positive JSON float.
	wide := AssessmentSegment{
		FirstAt: machineNow.Add(123456789 * time.Nanosecond), LastAt: machineNow.Add(123456789 * time.Nanosecond),
		Watermark: new(int64(math.MinInt64)), Count: math.MaxInt64, Causes: math.MaxUint32,
		Have: new(0.0000012345678901234567), Need: new(math.MaxInt),
	}
	actions := []Action{"", ActionEnter, ActionRenew, ActionRelease, ActionTrial, ActionTrialPass, ActionTrialFail, ActionTrialTimeout, ActionReset, ActionRelax, ActionClear}
	reasons := []AssessmentReason{ReasonPeerShort, ReasonOwnShort, ReasonWithin, ReasonPending, ReasonTrialOpen, ReasonOver, ReasonOther}
	var largest int
	for _, kind := range []EventKind{EventRegular, EventTrial, EventReview} {
		for _, action := range actions {
			for _, reason := range reasons {
				wide.Kind, wide.Action, wide.Reason = kind, action, reason
				for i := range state.LastAssessment {
					state.LastAssessment[i] = wide
				}
				data, err := json.Marshal(state.LastAssessment)
				require.NoError(t, err)
				largest = max(largest, len(data))
			}
		}
	}
	require.LessOrEqual(t, largest, 2048)
	t.Logf("compact JSON bytes: one own_short=%d, eight maximal-width segments=%d", len(typical), largest)
}

func TestAssessmentUnmappedCauseRoundTrip(t *testing.T) {
	unknown := RuleBUnknownReason(strings.Repeat("unregistered", 300))
	for _, tc := range []struct {
		name   string
		raw    []RuleBUnknownReason
		reason AssessmentReason
		names  []string
	}{
		{"only unknown", []RuleBUnknownReason{unknown, "second_unknown"}, ReasonOther, []string{"other"}},
		{"retired and unknown", []RuleBUnknownReason{RuleBNoPeers, unknown}, ReasonOther, []string{"no_peers", "other"}},
		{"unknown and retired", []RuleBUnknownReason{unknown, RuleBNoPeers}, ReasonOther, []string{"no_peers", "other"}},
		{"unknown and within", []RuleBUnknownReason{unknown, RuleBAbsoluteUnconfirmed}, ReasonWithin, []string{"absolute_unconfirmed", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Basis{Line: DimensionTTFO, TTFT: &TTFTEvidence{Causes: tc.raw}}
			h := RecordAssessment(LineState{}, machineNow, EventRegular, VerdictUnknown, "", b)
			data, err := json.Marshal(h)
			require.NoError(t, err)
			require.NotContains(t, string(data), "unregistered")
			var after []AssessmentSegment
			require.NoError(t, json.Unmarshal(data, &after))
			require.Equal(t, tc.reason, after[0].Reason)
			require.Equal(t, tc.names, after[0].CauseNames())
			require.Nil(t, after[0].Have)
			require.Nil(t, after[0].Need)
		})
	}
}

func TestAssessmentDecodesHistoricalPeerCauses(t *testing.T) {
	const stored = `{"f":"2026-01-01T12:00:00Z","l":"2026-01-01T12:00:00Z","n":1,"k":0,"r":"peer_short","c":1287,"h":9.25,"t":20}`
	var history AssessmentSegment
	require.NoError(t, json.Unmarshal([]byte(stored), &history))
	require.Equal(t, ReasonPeerShort, history.Reason)
	require.Equal(t, []string{"no_peers", "all_peers_rear", "peer_known_insufficient", "relative_unconfirmed", "relative_veto"}, history.CauseNames())
	require.Equal(t, new(9.25), history.Have)
	require.Equal(t, new(20), history.Need)
	encoded, err := json.Marshal(history)
	require.NoError(t, err)
	require.JSONEq(t, stored, string(encoded))
}

func TestAssessmentWatermarkTolerance(t *testing.T) {
	b := Basis{Line: DimensionCache, Cache: &CacheAssessment{Turns: 7}}
	h := RecordAssessment(LineState{}, machineNow, EventRegular, VerdictUnknown, "", b)
	for _, tc := range []struct {
		offset time.Duration
		same   bool
	}{
		{-2 * time.Microsecond, false}, {-1501, false}, {-1499, true}, {-1000, true}, {-999, true}, {-600, true}, {-499, true},
		{0, true}, {499, true}, {600, true}, {999, true}, {1000, true}, {1499, true}, {1501, false}, {2 * time.Microsecond, false},
	} {
		t.Run(tc.offset.String(), func(t *testing.T) {
			prev := LineState{LastAssessment: h, EvaluatedAt: machineNow.Add(tc.offset)}
			require.Equal(t, tc.same, h[0].MatchesWatermark(prev.EvaluatedAt))
			next := RecordAssessment(prev, machineNow.Add(RegularInterval), EventRegular, VerdictUnknown, "", b)
			if tc.same {
				require.Len(t, next, 1)
				require.EqualValues(t, 2, next[0].Count)
				require.Equal(t, machineNow, next[0].FirstAt)
			} else {
				require.Len(t, next, 2)
				require.EqualValues(t, 1, next[1].Count)
			}
		})
	}
}

func TestAssessmentCacheTrialNeedsFortyTurns(t *testing.T) {
	for _, turns := range []int{25, 40} {
		b := Basis{Line: DimensionCache, Cache: &CacheAssessment{Turns: turns, MinRatio: .8, Ratio: .8, Verdict: VerdictPass}}
		prev := LineState{LineKey: LineKey{Line: DimensionCache}, Trial: true,
			Epoch: machineNow.Add(-15 * time.Minute), TrialUntil: machineNow.Add(45 * time.Minute)}
		next, action := Step(prev, EventTrial, VerdictPass, b, machineNow)
		require.Len(t, next.LastAssessment, 1)
		s := next.LastAssessment[0]
		if turns < 40 {
			require.Empty(t, action)
			require.True(t, next.Trial)
			require.Equal(t, ReasonTrialOpen, s.Reason)
			require.Equal(t, []string{cacheTurnsShort}, s.CauseNames())
			require.Equal(t, new(float64(turns)), s.Have)
			require.Equal(t, new(40), s.Need)
		} else {
			require.Equal(t, ActionTrialPass, action)
			require.False(t, next.Trial)
			require.Equal(t, ReasonWithin, s.Reason)
			require.Empty(t, s.CauseNames())
			require.Nil(t, s.Need)
		}
	}
}

func TestAssessmentHistoricalClockReadOnly(t *testing.T) {
	const stored = "{\"w\":1767268800000000,\"f\":\"2026-01-01T12:00:00Z\",\"l\":\"2026-01-01T12:00:00Z\",\"n\":1,\"k\":3,\"a\":\"clear\",\"r\":\"within\"}"
	var segment AssessmentSegment
	require.NoError(t, json.Unmarshal([]byte(stored), &segment))
	require.Equal(t, EventClock, segment.Kind)
	require.Equal(t, ActionClear, segment.Action)
	encoded, err := json.Marshal(segment)
	require.NoError(t, err)
	require.JSONEq(t, stored, string(encoded))
	prev := LineState{LineKey: LineKey{Line: DimensionTTFO}, EvaluatedAt: segment.LastAt, LastAssessment: []AssessmentSegment{segment}}
	next, action := Step(prev, segment.Kind, VerdictUnknown, Basis{}, segment.LastAt.Add(time.Minute))
	require.Empty(t, action)
	require.Equal(t, prev, next, "a historical clock must not change state or its assessment watermark")
}
