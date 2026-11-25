//go:build unit

package service

import (
	"context"
	"encoding/json"
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

type resetFailureStore struct {
	SchedQualityStore
	err error
}

func (s resetFailureStore) RunReset(context.Context, int64) (time.Time, []SchedTrialGroup, error) {
	return time.Time{}, nil, s.err
}

type resetRebuildFailureStore struct {
	SchedQualityStore
	committed bool
}

func (s *resetRebuildFailureStore) RunReset(ctx context.Context, id int64) (time.Time, []SchedTrialGroup, error) {
	at, deleted, err := s.SchedQualityStore.RunReset(ctx, id)
	s.committed = err == nil
	return at, deleted, err
}
func (s *resetRebuildFailureStore) LoadRows(ctx context.Context) ([]q.LineState, []SchedOverride, map[int64]time.Time, error) {
	if s.committed {
		return nil, nil, nil, errors.New("rebuild read failed")
	}
	return s.SchedQualityStore.LoadRows(ctx)
}

func TestEngineResetReleasesOnlyAfterCommit(t *testing.T) {
	for _, mode := range []string{"success", "db_failure", "release_failure", "rebuild_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newQualityFixture(t)
			f.badTTFT()
			f.cycle()
			probe := &engineLeaseProbe{}
			probe.beforeRelease = func() {
				require.NotZero(t, f.store.Resets[1])
				for _, row := range f.store.Rows {
					require.NotEqual(t, int64(1), row.AccountID)
				}
			}
			f.e.SetTrialLeases(probe)
			switch mode {
			case "db_failure":
				f.e.store = resetFailureStore{f.store, errors.New("db reset failed")}
			case "release_failure":
				probe.err = errors.New("Redis unavailable")
			case "rebuild_failure":
				f.e.store = &resetRebuildFailureStore{SchedQualityStore: f.store}
			}
			_, err := f.e.Reset(context.Background(), 1)
			if mode == "db_failure" {
				require.ErrorContains(t, err, "db reset failed")
				require.Empty(t, probe.released)
				require.Empty(t, f.store.Resets)
				return
			}
			if mode == "rebuild_failure" {
				require.ErrorContains(t, err, "rebuild read failed")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []schedQualityKey{{1, "synthetic-model", 10}}, probe.released)
		})
	}
}

type insertBeforeResetStore struct {
	SchedQualityStore
	insert func()
}

func (s insertBeforeResetStore) RunReset(ctx context.Context, id int64) (time.Time, []SchedTrialGroup, error) {
	s.insert()
	return s.SchedQualityStore.RunReset(ctx, id)
}

func TestEngineResetFencesGroupsInsertedBeforeTransaction(t *testing.T) {
	f := newQualityFixture(t)
	leases := &engineLeaseProbe{}
	f.e.SetTrialLeases(leases)
	f.e.store = insertBeforeResetStore{f.store, func() {
		for _, line := range []q.Dimension{q.DimensionTTFO, q.DimensionError} {
			f.store.Rows = append(f.store.Rows, q.LineState{LineKey: q.LineKey{GroupID: 7, Model: "m", AccountID: 1, Line: line}, Trial: true})
		}
	}}
	_, err := f.e.Reset(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, f.store.Rows)
	require.Equal(t, []schedQualityKey{{1, "m", 7}}, leases.released)
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
	require.Equal(t, 2, b.Error.Unknown, "completed retries stay unknown until their durable evidence is available")
	require.Zero(t, b.Error.Clear, "finished but not inserted evidence is not yet usable")
	_, b = f.e.assessLine(d, row, q.EventRegular, at.Add(20*time.Second), nil)
	require.Equal(t, 1, b.Error.Clear)
	require.Equal(t, 1, b.Error.Unknown, "the observer-only retry is still not durable")
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
	lines := SchedAdminGroupModel(row, false).Lines
	require.Equal(t, q.NewSampleCounts(1, 3), lines.TTFO.SampleCounts)
	require.InDelta(t, lines.TTFO.Evidence.N/3, *lines.TTFO.Evidence.NLocal, 1e-12)
	require.Equal(t, q.NewSampleCounts(1, 4), lines.Error.SampleCounts)
	require.Equal(t, q.NewSampleCounts(0, 0), lines.Cache.SampleCounts)
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

func TestEngineRestartReplaysOverdueDeadlinesWithoutExtendingTrial(t *testing.T) {
	f := newQualityFixture(t)
	until := f.now.Add(-2 * time.Hour).Truncate(time.Minute)
	entry := until.Add(-20 * time.Minute)
	f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Admissions: 1, Entry: entry, LastRear: entry, Until: until, EvaluatedAt: entry}}
	f.cycle()
	require.False(t, f.view().TTFO.Trial)
	require.Equal(t, q.IdentityUnknown, f.view().TTFO.Identity)
	require.Equal(t, until.Add(q.TrialDuration), f.line(q.DimensionTTFO).Epoch)
	require.Len(t, f.store.Decisions, 2)
	require.Equal(t, q.ActionTrial, f.store.Decisions[0].Action)
	require.Equal(t, until, f.store.Decisions[0].At)
	require.Equal(t, until.Add(q.TrialDuration), f.store.Decisions[0].Until)
	require.Equal(t, q.ActionTrialTimeout, f.store.Decisions[1].Action)
	require.Equal(t, until.Add(q.TrialDuration), f.store.Decisions[1].At)
	require.True(t, f.store.Decisions[1].Until.IsZero())
	f.e.processedAt = time.Time{}
	f.cycle()
	require.Len(t, f.store.Decisions, 2)
}

func TestEngineUninsertedRetryHidesEarlierFailure(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable-future-insertion", true: "observer-only"}[observed], func(t *testing.T) {
			f := newQualityFixture(t)
			at := f.now.Add(-10 * time.Second)
			first := engineSamples(1, at.Add(-time.Minute), false)[0]
			first.RequestID = "retry"
			first.Outcome = q.OutcomeAccountFault
			first.Class = q.ClassUpstream
			retry := engineSamples(1, at.Add(-time.Second), false)[0]
			retry.RequestID = "retry"
			retry.InsertedAt = at.Add(time.Second)
			f.e.window.merge([]q.AttemptSample{first}, f.now)
			if observed {
				retry.InsertedAt = time.Time{}
				f.e.Observe(retry)
				f.e.window.take(f.now)
			} else {
				f.e.window.merge([]q.AttemptSample{retry}, f.now)
			}
			d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
			require.NoError(t, err)
			_, basis := f.e.assessLine(d, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: first.Model, Line: q.DimensionError}}, q.EventRegular, at, nil)
			require.Zero(t, basis.Error.Failed)
			require.Zero(t, basis.Error.Clear)
			require.Equal(t, 1, basis.Error.Unknown)
		})
	}
}
func TestEngineDecisionDeadlinesAndCounters(t *testing.T) {
	output := captureSchedObsLogs(t)
	obs := NewSchedObservabilityService()
	f := newQualityFixture(t)
	f.e.obs = obs
	next := q.LineState{Until: f.now.Add(20 * time.Minute), TrialUntil: f.now.Add(time.Hour)}
	for _, tc := range []struct {
		action q.Action
		until  time.Time
	}{
		{q.ActionEnter, next.Until}, {q.ActionRenew, next.Until}, {q.ActionTrialFail, next.Until},
		{q.ActionTrial, next.TrialUntil}, {q.ActionRelease, time.Time{}}, {q.ActionTrialPass, time.Time{}},
		{q.ActionTrialTimeout, time.Time{}}, {q.ActionClear, time.Time{}}, {q.ActionRelax, time.Time{}}, {q.ActionReset, time.Time{}},
	} {
		require.Equal(t, tc.until, schedDecisionUntil(tc.action, next), tc.action)
		f.e.countDecision(tc.action)
	}
	obs.summary(context.Background())
	record := schedObsLogRecords(t, output)[0]
	require.Equal(t, float64(3), record["cooled"])
	require.Equal(t, float64(3), record["released"])
	require.Equal(t, float64(1), record["trial"])
	require.Equal(t, float64(1), record["relax"])
	require.Equal(t, float64(0), record["clear"], "retired quiet-clock action must not increment counters")
}

// Even with generation fencing, an unsuccessful Redis cleanup can leave seats
// touched through a still-fresh old view. Reassess that residual window if
// the first cooldown no longer outlasts both view staleness and lease idleness.
func TestSchedTrialResetResidualWindowFitsFirstCooldown(t *testing.T) {
	require.Greater(t, q.RearDuration, TrialLeaseIdle+q.ViewMaxAge)
}

type timeoutSeatProbe struct {
	engineLeaseProbe
	stall   bool
	at      time.Time
	err     error
	reads   []schedQualityKey
	bounded bool
}

func (p *timeoutSeatProbe) LastFull(ctx context.Context, a int64, m string, g int64) (time.Time, error) {
	_, p.bounded = ctx.Deadline()
	p.reads = append(p.reads, schedQualityKey{a, m, g})
	if p.stall && p.bounded {
		<-ctx.Done()
		return time.Time{}, ctx.Err()
	}
	return p.at, p.err
}

// ReplayTrialTimeoutBasisForTest projects a persisted timeout decision for clock-boundary tests.
func ReplayTrialTimeoutBasisForTest(t *testing.T, leases SchedTrialLeases, start, horizon time.Time) *SchedAdminBasis {
	t.Helper()
	end := start.Add(q.TrialDuration)
	f := newQualityFixture(t)
	f.now = horizon
	f.e.SetTrialLeases(leases)
	key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}
	f.store.Rows = []q.LineState{{LineKey: key, Trial: true, Epoch: start, TrialUntil: end, EvaluatedAt: end.Add(-time.Minute)}}
	f.cycle()
	require.Len(t, f.store.Decisions, 1)
	decision := f.store.Decisions[0]
	require.Equal(t, key, decision.Key)
	require.Equal(t, q.ActionTrialTimeout, decision.Action)
	require.Equal(t, &decision.Basis, f.line(key.Line).Basis)
	raw, err := json.Marshal(decision.Basis)
	require.NoError(t, err)
	var stored q.Basis
	require.NoError(t, json.Unmarshal(raw, &stored))
	return SchedAdminDecisionBasis(stored)
}

func TestObserveTimeoutSeatEvidenceAtTrialDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		offset     time.Duration
		missing    bool
		err        error
	}{
		{"start", "unknown", 0, false, nil},
		{"end", "unknown", q.TrialDuration, false, nil},
		{"start minus two seconds", "unknown", -2 * time.Second, false, nil},
		{"start plus two seconds", "unknown", 2 * time.Second, false, nil},
		{"end minus two seconds", "unknown", q.TrialDuration - 2*time.Second, false, nil},
		{"end plus two seconds", "unknown", q.TrialDuration + 2*time.Second, false, nil},
		{"before start band", "not_observed", -2*time.Second - time.Millisecond, false, nil},
		{"after start band", "full", 2*time.Second + time.Millisecond, false, nil},
		{"before end band", "full", q.TrialDuration - 2*time.Second - time.Millisecond, false, nil},
		{"after end band", "unknown", q.TrialDuration + 2*time.Second + time.Millisecond, false, nil},
		{"absent", "not_observed", 0, true, nil},
		{"storage error", "unknown", 0, true, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			horizon := start.Add(q.TrialDuration + 10*time.Second)
			probe := &timeoutSeatProbe{err: tc.err}
			if !tc.missing {
				probe.at = start.Add(tc.offset)
			}
			basis := ReplayTrialTimeoutBasisForTest(t, probe, start, horizon)
			require.Equal(t, new(tc.want), basis.TrialSeats)
			require.Equal(t, q.NewSampleCounts(0, 0), basis.SampleCounts)
			require.True(t, probe.bounded)
			require.Equal(t, []schedQualityKey{{1, "synthetic-model", 10}}, probe.reads)
		})
	}
}

func TestObserveTimeoutSeatReadBudgetSharedByReplay(t *testing.T) {
	f := newQualityFixture(t)
	end := f.now.Add(-10 * time.Second)
	for _, dim := range []q.Dimension{q.DimensionTTFO, q.DimensionError, q.DimensionCache} {
		f.store.Rows = append(f.store.Rows, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: dim},
			Trial: true, Epoch: end.Add(-q.TrialDuration), TrialUntil: end, EvaluatedAt: end.Add(-time.Minute)})
	}
	probe := &timeoutSeatProbe{stall: true}
	f.e.SetTrialLeases(probe)
	f.cycle()
	require.True(t, probe.bounded)
	require.Len(t, probe.reads, 1, "an unavailable Redis must not cost one timeout per trial")
	require.Len(t, f.store.Decisions, 3)
	for _, decision := range f.store.Decisions {
		require.Equal(t, q.ActionTrialTimeout, decision.Action)
		require.Equal(t, new("unknown"), decision.Basis.TrialSeats)
	}
}
