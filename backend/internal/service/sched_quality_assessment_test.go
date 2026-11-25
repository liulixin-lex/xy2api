//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedAssessmentPublishedOnlyAfterCommit(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = engineSamples(1, f.now.Add(-time.Minute), true)
	f.cycle()
	lines := func() SchedAdminLines {
		t.Helper()
		rows := f.e.Snapshot().RowsForAccount(1)
		require.Len(t, rows, 1)
		return SchedAdminGroupModel(rows[0], false).Lines
	}
	first := lines()
	require.Equal(t, q.ReasonPeerShort, first.TTFO.LastEval.Reason)
	require.Equal(t, new(0.0), first.TTFO.LastEval.Have)
	require.Equal(t, new(20), first.TTFO.LastEval.Need)
	require.Equal(t, q.ReasonOwnShort, first.Cache.LastEval.Reason)
	require.Equal(t, new(50), first.Error.LastEval.Need)
	firstAt := first.Error.LastEval.At
	require.EqualValues(t, 1, first.Error.LastEval.Count)
	f.now = f.now.Add(2 * time.Minute)
	f.ledger.Rows = append(f.ledger.Rows, engineSamples(1, f.now.Add(-time.Second), true)...)
	f.cycle()
	require.Equal(t, first.Error.LastEval, lines().Error.LastEval)
	f.now = f.now.Add(3 * time.Minute)
	f.store.CommitErr = errors.New("synthetic commit failure")
	require.ErrorIs(t, f.e.cycle(t.Context()), f.store.CommitErr)
	require.Equal(t, first.Error.LastEval, lines().Error.LastEval)
	require.Equal(t, first.Error.LastEval, SchedAdminLastEvalFromState(f.line(q.DimensionError)))
	f.store.CommitErr = nil
	f.cycle()
	last := lines().Error.LastEval
	require.EqualValues(t, 2, last.Count)
	require.Equal(t, firstAt, last.Since)
	require.NotEqual(t, last.At, firstAt)
	require.Empty(t, f.store.Decisions)
}

func TestSchedAssessmentLastEvalJSON(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := q.LineState{EvaluatedAt: at}
	check := func(want string) {
		t.Helper()
		line := SchedAdminGroupModel(SchedGroupRow{ErrorLastEval: SchedAdminLastEvalFromState(r)}, false).Lines.Error
		data, err := json.Marshal(line)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &fields))
		require.JSONEq(t, want, string(fields["last_eval"]))
	}
	check("null")
	b := q.Basis{Line: q.DimensionError, Error: &q.ErrorAssessment{Clear: 14}}
	r.LastAssessment = q.RecordAssessment(q.LineState{}, at, q.EventRegular, q.VerdictUnknown, "", b)
	check(`{"at":"2026-01-01T12:00:00Z","acted":false,"action":"","reason":"own_short","causes":["clear_insufficient"],"have":14,"need":50,"since":"2026-01-01T12:00:00Z","count":1}`)
	next := at.Add(q.RegularInterval)
	r.LastAssessment = q.RecordAssessment(r, next, q.EventRegular, q.VerdictFail, q.ActionEnter, b)
	r.EvaluatedAt = next
	check(`{"at":"2026-01-01T12:05:00Z","acted":true,"action":"enter","reason":"over","causes":[],"have":null,"need":null,"since":"2026-01-01T12:05:00Z","count":1}`)
	r.EvaluatedAt = next.Add(time.Second)
	check("null")
}

func TestSchedAssessmentRelaxStartsRun(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = engineSamples(1, f.now.Add(-time.Minute), true)
	f.cycle()
	before := f.line(q.DimensionTTFO).LastAssessment
	require.Len(t, before, 1)
	f.now = f.now.Add(2*time.Minute + 123456789*time.Nanosecond)
	rule := q.DefaultRuleBParams()
	rule.Confidence = .9
	_, err := f.e.UpdateTTFTRule(t.Context(), 0, rule)
	require.NoError(t, err)
	relaxed := f.line(q.DimensionTTFO)
	require.Len(t, relaxed.LastAssessment, 2)
	require.Equal(t, before[0], relaxed.LastAssessment[0])
	seg := relaxed.LastAssessment[1]
	require.Equal(t, q.EventRegular, seg.Kind)
	require.Equal(t, q.ReasonPeerShort, seg.Reason)
	require.Empty(t, seg.Action)
	require.Equal(t, f.now.Round(time.Microsecond), seg.FirstAt)
	require.Equal(t, f.now.Round(time.Microsecond), seg.LastAt)
	require.EqualValues(t, 1, seg.Count)
	relaxAt := f.now.Round(time.Microsecond)
	f.now = f.now.Add(3 * time.Minute)
	f.cycle()
	next := f.line(q.DimensionTTFO)
	require.Len(t, next.LastAssessment, 2)
	require.Equal(t, before[0], next.LastAssessment[0])
	last := SchedAdminLastEvalFromState(next)
	require.NotNil(t, last)
	require.EqualValues(t, 2, last.Count)
	require.Equal(t, relaxAt.Format(time.RFC3339Nano), last.Since)
}

func TestSchedAssessmentDTOAcrossRawWatermarkOffsets(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	b := q.Basis{Line: q.DimensionCache, Cache: &q.CacheAssessment{Turns: 7}}
	first, _ := q.Step(q.LineState{LineKey: q.LineKey{Line: q.DimensionCache}}, q.EventRegular, q.VerdictUnknown, b, at)
	want := SchedAdminLastEvalFromState(first)
	require.NotNil(t, want)
	for _, delta := range []time.Duration{-1499, -1000, -999, -600, -499, 499, 600, 999, 1000, 1499} {
		t.Run(delta.String(), func(t *testing.T) {
			prev := first
			prev.EvaluatedAt = at.Add(delta)
			require.Equal(t, want, SchedAdminLastEvalFromState(prev))
			next, _ := q.Step(prev, q.EventRegular, q.VerdictUnknown, b, at.Add(q.RegularInterval))
			got := SchedAdminLastEvalFromState(next)
			require.NotNil(t, got)
			require.EqualValues(t, 2, got.Count)
			require.Equal(t, want.Since, got.Since)
		})
	}
}

func TestSchedAssessmentPolicySaveKeepsHistory(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = errorRecoverySamples(20, 20, f.now.Add(-30*time.Second))
	f.cycle()
	f.now = f.now.Add(q.RegularInterval)
	f.cycle()
	before := f.line(q.DimensionError)
	require.Equal(t, q.ActionEnter, before.LastAssessment[len(before.LastAssessment)-1].Action)
	f.now = before.Until.Add(10 * time.Second)
	f.cycle()
	before = f.line(q.DimensionError)
	require.Len(t, before.LastAssessment, 3)
	require.Equal(t, q.ActionTrial, before.LastAssessment[2].Action)
	f.now = f.now.Add(2 * time.Second)
	// This batch is visible to the explicit rejudge, after the periodic horizon.
	f.ledger.Rows = append(f.ledger.Rows, errorRecoverySamples(52, 0, f.now.Add(-time.Second))...)
	policy := f.store.Policies[10].Policy
	policy.MaxErrorRate = .1
	_, err := f.e.UpdatePolicy(t.Context(), 10, 0, policy)
	require.NoError(t, err)
	after := f.line(q.DimensionError)
	require.Len(t, after.LastAssessment, 4)
	require.Equal(t, before.LastAssessment, after.LastAssessment[:3])
	seg := after.LastAssessment[3]
	require.Equal(t, q.EventRegular, seg.Kind)
	require.Equal(t, q.ReasonWithin, seg.Reason)
	want := &SchedAdminLastEval{At: f.now.Format(time.RFC3339Nano), Since: f.now.Format(time.RFC3339Nano), Count: 1, Reason: q.ReasonWithin, Causes: []string{}}
	require.Equal(t, want, SchedAdminLastEvalFromState(after))
	published := SchedAdminGroupModel(f.e.Snapshot().RowsForAccount(1)[0], false).Lines.Error.LastEval
	require.Equal(t, want, published)
}
