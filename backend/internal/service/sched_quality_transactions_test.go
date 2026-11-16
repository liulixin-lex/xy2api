//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type engineLeaseProbe struct {
	SchedTrialLeases
	released      []schedQualityKey
	beforeRelease func()
	err           error
}

func (p *engineLeaseProbe) ReleaseGroup(_ context.Context, a int64, m string, g int64) error {
	if p.beforeRelease != nil {
		p.beforeRelease()
	}
	p.released = append(p.released, schedQualityKey{a, m, g})
	return p.err
}
func TestEngineResetReleasesAllModelsBeforeDeletingStates(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.cycle()
	probe := &engineLeaseProbe{err: errors.New("synthetic Redis outage")}
	probe.beforeRelease = func() { require.NotEmpty(t, f.store.Rows, "release precedes deletion") }
	f.e.SetTrialLeases(probe)
	_, err := f.e.Reset(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, []schedQualityKey{{1, "synthetic-model", 10}}, probe.released)
	for _, r := range f.store.Rows {
		require.NotEqual(t, int64(1), r.AccountID)
	}
}
func TestEngineReleaseWaitsForLastTrialLine(t *testing.T) {
	f := newQualityFixture(t)
	probe := &engineLeaseProbe{}
	f.e.SetTrialLeases(probe)
	first := q.LineState{LineKey: q.LineKey{AccountID: 1, Model: "m", GroupID: 10, Line: q.DimensionTTFO}, Trial: true}
	second := first
	second.Line = q.DimensionError
	before := []q.LineState{first, second}
	first.Trial = false
	f.e.releaseEndedTrials(context.Background(), before, []q.LineState{first, second})
	require.Empty(t, probe.released)
	second.Trial = false
	f.e.releaseEndedTrials(context.Background(), before, []q.LineState{first, second})
	require.Len(t, probe.released, 1)
}
func TestEngineInFlightAtReplayTime(t *testing.T) {
	f := newQualityFixture(t)
	at := f.now.Add(-10 * time.Second)
	finished := engineSamples(1, at.Add(time.Second), true)[0]
	finished.InsertedAt = at.Add(20 * time.Second)
	f.e.window.merge([]q.AttemptSample{finished}, f.now)
	local := finished
	local.AttemptID = "local"
	local.RequestID = "local"
	f.e.Observe(local)
	f.e.window.take(f.now)
	d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
	require.NoError(t, err)
	row := q.LineState{LineKey: q.LineKey{AccountID: 1, Model: finished.Model, GroupID: 10, Line: q.DimensionError}}
	_, b := f.e.assessLine(d, row, q.EventRegular, at, nil)
	require.Equal(t, 2, b.Error.Unknown)
	_, b = f.e.assessLine(d, row, q.EventRegular, at.Add(2*time.Second), nil)
	require.Zero(t, b.Error.Unknown)
	require.Zero(t, b.Error.Clear, "finished but not inserted evidence is not yet usable")
	_, b = f.e.assessLine(d, row, q.EventRegular, at.Add(20*time.Second), nil)
	require.Equal(t, 1, b.Error.Clear)
}
func TestEngineErrorSourceGroupsFollowFinalHundredRequests(t *testing.T) {
	f := newQualityFixture(t)
	at := f.now.Add(-time.Minute)
	rows := engineSamples(101, at, false)
	for i := range rows {
		rows[i].FinishedAt = at.Add(time.Duration(i) * time.Millisecond)
		rows[i].StartedAt = rows[i].FinishedAt.Add(-time.Second)
	}
	rows[0].GroupID = 20
	f.e.window.merge(rows, f.now)
	d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
	require.NoError(t, err)
	_, b := f.e.assessLine(d, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}}, q.EventRegular, f.now, nil)
	require.Equal(t, 100, b.Error.Clear)
	require.Equal(t, []int64{10}, b.SourceGroups)
}

func TestEngineRequestOriginSurvivesEvidenceCut(t *testing.T) {
	f := newQualityFixture(t)
	at := f.now.Add(-10 * time.Second)
	cut := at.Add(-time.Minute)
	first := engineSamples(1, at.Add(-2*time.Minute), false)[0]
	last := engineSamples(1, at.Add(-time.Second), false)[0]
	first.RequestID, last.RequestID = "same-request", "same-request"
	first.GroupID = 20
	f.e.window.merge([]q.AttemptSample{first, last}, f.now)
	d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
	require.NoError(t, err)
	_, b := f.e.assessLine(d, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: first.Model, Line: q.DimensionError}, Epoch: cut}, q.EventRegular, at, nil)
	require.Zero(t, b.Error.Clear)
	require.Empty(t, b.SourceGroups)
}
func TestEngineRuleBEvaluationErrorRemainsUnknown(t *testing.T) {
	f := newQualityFixture(t)
	d, err := f.e.inputs(context.Background(), f.now, nil)
	require.NoError(t, err)
	f.e.rule.Params.Confidence = 0
	verdict, b := f.e.assessLine(d, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic", Line: q.DimensionTTFO}}, q.EventRegular, f.now, nil)
	require.Equal(t, q.VerdictUnknown, verdict)
	require.Nil(t, b.TTFT)
}

func TestEngineSnapshotKeepsTTFTSourceGroupsSeparate(t *testing.T) {
	f := newQualityFixture(t)
	rows := engineSamples(4, f.now.Add(-time.Minute), true)
	rows[0].GroupID = 30
	rows[1].GroupID = 10
	rows[2].GroupID = 30
	rows[3].GroupID = 20
	rows[3].Stream = false
	f.ledger.Rows = rows
	f.cycle()
	row := f.e.Snapshot().RowsForAccount(1)[0]
	require.Equal(t, []int64{10, 30}, row.TTFTSourceGroups)
	require.Equal(t, []int64{10, 20, 30}, row.Error.SourceGroups)
	require.Equal(t, []int64{10, 20, 30}, row.SourceGroups)
}

func TestEngineSlowBoundaryMatchesRuleB(t *testing.T) {
	at := time.Unix(1000, 0)
	ms := 1001
	a := q.AttemptSample{Stream: true, FinishedAt: at, Outcome: q.OutcomeOK, TTFOMs: &ms}
	require.False(t, schedSlow(a, 1.001), "known first output equal to the limit is not slow")
	ms = 1002
	require.True(t, schedSlow(a, 1.001))
	a.TTFOMs = nil
	a.Outcome = q.OutcomeTimeout
	a.CensoredMs = &ms
	ms = 1001
	require.True(t, schedSlow(a, 1.001), "censoring at the limit proves slow")
	ms = 1000
	require.False(t, schedSlow(a, 1.001))
}
