//go:build unit

package schedquality

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
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
		{RuleBNoPeers, ReasonPeerShort, 0, 20},
		{RuleBAllPeersRear, ReasonPeerShort, 0, 20},
		{RuleBPeerKnownInsufficient, ReasonPeerShort, 9.25, 20},
		{RuleBNoSamples, ReasonOwnShort, 0, 20},
		{RuleBMassInsufficient, ReasonOwnShort, 8.75, 20},
		{RuleBSessionsInsufficient, ReasonOwnShort, 2, 3},
		{RuleBSlowSessionsInsufficient, ReasonOwnShort, 1, 2},
		{RuleBAbsoluteUnconfirmed, ReasonWithin, 0, 0},
		{RuleBRelativeUnconfirmed, ReasonWithin, 0, 0},
		{RuleBWorstMeanAboveTolerance, ReasonWithin, 0, 0},
		{RuleBRelativeVeto, ReasonWithin, 0, 0},
	}
	var tested []string
	for _, tc := range cases {
		tested = append(tested, string(tc.cause))
		t.Run(string(tc.cause), func(t *testing.T) {
			e := &TTFTEvidence{N: 8.75, S: 2, D: 1, Causes: []RuleBUnknownReason{tc.cause}}
			b := Basis{Line: DimensionTTFO, TTFT: e}
			if tc.cause == RuleBPeerKnownInsufficient {
				b.PeerKnown = 9.25
			}
			if tc.cause == RuleBNoSamples {
				e.N = 0
			}
			seg := RecordAssessment(LineState{}, machineNow, EventRegular, VerdictUnknown, "", b)[0]
			require.Equal(t, tc.reason, seg.Reason)
			require.Equal(t, []string{string(tc.cause)}, seg.Causes.Names())
			if tc.need > 0 {
				require.Equal(t, new(tc.have), seg.Have)
				require.Equal(t, new(tc.need), seg.Need)
			} else {
				require.Nil(t, seg.Have)
				require.Nil(t, seg.Need)
			}
		})
	}
	// Read the actual enum declarations so adding a cause requires a classification case.
	file, err := parser.ParseFile(token.NewFileSet(), "rule_b.go", nil, 0)
	require.NoError(t, err)
	var declared []string
	for _, group := range file.Decls {
		decl, ok := group.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			continue
		}
		for _, spec := range decl.Specs {
			value, ok := spec.(*ast.ValueSpec)
			require.True(t, ok)
			typ, ok := value.Type.(*ast.Ident)
			if !ok || typ.Name != "RuleBUnknownReason" {
				continue
			}
			for _, expr := range value.Values {
				literal, ok := expr.(*ast.BasicLit)
				require.True(t, ok)
				name, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				declared = append(declared, name)
			}
		}
	}
	require.ElementsMatch(t, declared, tested)
}

func TestAssessmentLineOutcomes(t *testing.T) {
	cache := func(c CacheAssessment) Basis { return Basis{Line: DimensionCache, Cache: &c} }
	errLine := func(e ErrorAssessment) Basis { return Basis{Line: DimensionError, Error: &e} }
	recovery := errLine(ErrorAssessment{Clear: 60, RecoveryOverLimit: true})
	ttft := Basis{Line: DimensionTTFO, PeerKnown: 2.5, TTFT: &TTFTEvidence{
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
		{"cache-turns", cache(CacheAssessment{Turns: 12, MinRatio: .8, LowSessions: 1}), EventRegular, VerdictUnknown, "", ReasonOwnShort, []string{cacheTurnsShort, cacheLowSessionsShort}, 12, 20},
		{"cache-low-sessions", cache(CacheAssessment{Turns: 22, MinRatio: .8, LowSessions: 1}), EventRegular, VerdictUnknown, "", ReasonOwnShort, []string{cacheLowSessionsShort}, 1, 2},
		{"clear", errLine(ErrorAssessment{Clear: 13, LowerPass: .1, Limit: .05}), EventRegular, VerdictUnknown, "", ReasonOwnShort, []string{errorClearShort, errorBoundsUnconfirmed}, 13, 50},
		{"bounds", errLine(ErrorAssessment{Clear: 50, LowerPass: .07, Limit: .05}), EventRegular, VerdictUnknown, "", ReasonWithin, []string{errorBoundsUnconfirmed}, 0, 0},
		{"recovery-blocked", recovery, EventReview, VerdictUnknown, ActionTrial, ReasonPending, []string{errorRecoveryOverLimit}, 0, 0},
		{"recovery-open", recovery, EventTrial, VerdictUnknown, "", ReasonTrialOpen, []string{errorRecoveryOverLimit}, 0, 0},
		{"recovery-timeout", recovery, EventTrial, VerdictUnknown, ActionTrialTimeout, ReasonPending, []string{errorRecoveryOverLimit}, 0, 0},
		{"peer-priority", ttft, EventReview, VerdictUnknown, ActionTrial, ReasonPeerShort, []string{string(RuleBPeerKnownInsufficient), string(RuleBSessionsInsufficient), string(RuleBRelativeVeto)}, 2.5, 20},
		{"trial-open", ttft, EventTrial, VerdictUnknown, "", ReasonTrialOpen, []string{string(RuleBPeerKnownInsufficient), string(RuleBSessionsInsufficient), string(RuleBRelativeVeto)}, 2.5, 20},
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
			require.Equal(t, tc.causes, s.Causes.Names())
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
	require.Equal(t, state.LastAssessment, RecordAssessment(state, state.EvaluatedAt.Add(time.Second), EventClock, VerdictUnknown, ActionClear, Basis{}))
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
	// Combine maximal field widths, including values wider than any real gated count.
	for i := range state.LastAssessment {
		s := &state.LastAssessment[i]
		s.FirstAt = machineNow.Add(123456789 * time.Nanosecond)
		s.LastAt = s.FirstAt
		s.Count = math.MaxInt64
		s.Kind = EventReview
		s.Action = ActionTrialTimeout
		s.Reason = ReasonTrialOpen
		s.Causes = math.MaxUint32
		s.Have = new(1.2345678901234567e-100)
		s.Need = new(math.MaxInt)
	}
	worst, err := json.Marshal(state.LastAssessment)
	require.NoError(t, err)
	require.LessOrEqual(t, len(worst), 2048)
	t.Logf("compact JSON bytes: one own_short=%d, eight maximal-width segments=%d", len(typical), len(worst))
}
