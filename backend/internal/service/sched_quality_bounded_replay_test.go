//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestEngineRecoveryBoundsExpiredTimers(t *testing.T) {
	for _, kind := range []string{"rear", "trial", "regular"} {
		t.Run(kind, func(t *testing.T) {
			f := newQualityFixture(t)
			f.now = f.now.Add(6 * time.Hour) // 18:00:10, retained replay starts at 13:50.
			horizon := f.now.Add(-10 * time.Second)
			floor := horizon.Add(-schedEvidenceRetention)
			first := floor.Add(-time.Hour) // Original deadline survives missing history.
			r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: floor.Add(-48 * time.Hour), Level: 2, Admissions: 2, LastRear: floor.Add(-25 * time.Hour)}
			var want q.Action
			switch kind {
			case "rear":
				r.Identity, r.Until = q.IdentityRear, floor.Add(-time.Hour)
				want = q.ActionTrial
			case "trial":
				r.Trial, r.TrialUntil = true, floor.Add(-time.Hour)
				want = q.ActionTrialTimeout
			case "regular":
				r.Level, r.Admissions = 0, 0
			}
			f.store.Rows = []q.LineState{r}
			// Entire population consists of expired rear keys, without any samples.
			if kind == "rear" {
				for id := int64(2); id <= 3; id++ {
					other := r
					other.AccountID = id
					f.store.Rows = append(f.store.Rows, other)
					f.store.Members[10] = append(f.store.Members[10], id)
				}
			}
			d, err := f.e.inputs(context.Background(), f.now, nil)
			require.NoError(t, err)
			_, _, _, decisions, err := f.e.replay(context.Background(), d, f.store.Rows, nil, time.Time{}, horizon)
			require.NoError(t, err)
			for _, decision := range decisions {
				require.True(t, decision.At.After(r.EvaluatedAt), decision)
			}
			if want != "" {
				require.NotEmpty(t, decisions)
				require.Equal(t, want, decisions[0].Action)
				require.Equal(t, first, decisions[0].At)
			} else {
				require.LessOrEqual(t, d.replayStats.batches, 50)
			}
			if kind == "rear" {
				for _, id := range f.store.Members[10] {
					found := false
					for _, decision := range decisions {
						found = found || decision.Key.AccountID == id && decision.Action == q.ActionTrial && decision.At.Equal(first)
					}
					require.True(t, found, "expired rear account %d must not get stuck", id)
				}
			}
			require.LessOrEqual(t, d.replayStats.batches, 125, "retention bounds the replay, including the new bounded trial")
			f.cycle()
			require.Zero(t, f.line(q.DimensionError).Level)
			require.Equal(t, r.Admissions, f.line(q.DimensionError).Admissions)
			rows := append([]q.LineState(nil), f.store.Rows...)
			saved := append([]q.Decision(nil), f.store.Decisions...)
			f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
			f.cycle()
			require.ElementsMatch(t, rows, f.store.Rows)
			require.Equal(t, saved, f.store.Decisions)
		})
	}
}

func TestEngineSaveDrainsOldConfiguration(t *testing.T) {
	for _, line := range []q.Dimension{q.DimensionError, q.DimensionTTFO} {
		t.Run(fmt.Sprint(line), func(t *testing.T) {
			run := func(drained bool) ([]q.LineState, []q.Decision) {
				f := newQualityFixture(t)
				start := f.now.Add(-10 * time.Second)
				f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: line}, EvaluatedAt: start}}
				p := f.store.Policies[10]
				p.Policy.MaxErrorRate = .9
				f.store.Policies[10] = p
				params := q.DefaultRuleBParams()
				params.Tolerance = .9
				f.store.Rule.Params = params
				n := 6
				if line == q.DimensionTTFO {
					n = 80
					f.store.Members[10] = []int64{1, 2}
				}
				f.ledger.Rows = engineSamples(n, start.Add(time.Minute), line == q.DimensionTTFO)
				for i := range f.ledger.Rows {
					a := &f.ledger.Rows[i]
					a.InsertedAt = a.FinishedAt
					a.Class = q.ClassUpstream
					if line == q.DimensionError {
						a.Outcome = q.OutcomeAccountFault
					} else {
						a.SessionKey = fmt.Sprintf("own-%d", i%3)
						if i%2 == 0 {
							ms := 100
							a.TTFOMs = &ms
						}
					}
				}
				if line == q.DimensionTTFO {
					for _, a := range append([]q.AttemptSample(nil), f.ledger.Rows...) {
						a.AccountID = 2
						a.AttemptID, a.RequestID, a.SessionKey = "peer-"+a.AttemptID, "peer-"+a.RequestID, "peer-"+a.SessionKey
						ms := 100
						a.TTFOMs = &ms
						f.ledger.Rows = append(f.ledger.Rows, a)
					}
				}
				f.now = start.Add(40*time.Minute + 10*time.Second)
				if drained {
					f.cycle()
				}
				if line == q.DimensionError {
					p.Policy.MaxErrorRate = .05
					_, err := f.e.UpdatePolicy(context.Background(), 10, 0, p.Policy)
					require.NoError(t, err)
				} else {
					params.Tolerance = .05
					result, err := f.e.UpdateTTFTRule(context.Background(), 0, params)
					require.NoError(t, err)
					require.Equal(t, q.RuleBTightened, result.Change)
				}
				require.Equal(t, start.Add(40*time.Minute), f.e.processedAt)
				require.NotEmpty(t, f.e.peerBuckets, "committed catchup buckets must be published")
				f.now = start.Add(50*time.Minute + 10*time.Second)
				f.cycle()
				require.NotEmpty(t, f.store.Decisions, "tightened rule must actually change the outcome")
				for _, decision := range f.store.Decisions {
					require.True(t, decision.At.After(start.Add(40*time.Minute)), "old configuration must govern the backlog")
				}
				return f.store.Rows, f.store.Decisions
			}
			rows, decisions := run(true)
			gotRows, gotDecisions := run(false)
			require.ElementsMatch(t, rows, gotRows)
			require.Equal(t, decisions, gotDecisions)
		})
	}
}

type failReplaySaveStore struct {
	*fakeSchedQualityStore
	saves int
}
type failReplaySaveTx struct {
	SchedQualityTx
	owner *failReplaySaveStore
}

var errReplaySave = errors.New("synthetic replay persistence failure")

func (s *failReplaySaveStore) RunExclusive(ctx context.Context, fn func(SchedQualityTx) error) error {
	return s.fakeSchedQualityStore.RunExclusive(ctx, func(tx SchedQualityTx) error {
		return fn(&failReplaySaveTx{SchedQualityTx: tx, owner: s})
	})
}
func (tx *failReplaySaveTx) InsertDecisions([]q.Decision) error { return errReplaySave }
func (tx *failReplaySaveTx) SavePolicy(g, v int64, p q.Policy) (SchedPolicyRecord, error) {
	tx.owner.saves++
	return tx.SchedQualityTx.SavePolicy(g, v, p)
}
func (tx *failReplaySaveTx) SaveTTFTRule(v int64, p q.RuleBParams) (SchedTTFTRuleRecord, error) {
	tx.owner.saves++
	return tx.SchedQualityTx.SaveTTFTRule(v, p)
}

func TestEngineReplayFailureAbortsConfigurationSave(t *testing.T) {
	for _, rule := range []bool{false, true} {
		t.Run(fmt.Sprint(rule), func(t *testing.T) {
			f := newQualityFixture(t)
			f.cycle()
			before := f.e.Snapshot()
			watermark := f.e.processedAt
			buckets := clonePeerBuckets(f.e.peerBuckets, f.now)
			policies := cloneSchedPolicies(f.store.Policies)
			rows := append([]q.LineState(nil), f.store.Rows...)
			store := &failReplaySaveStore{fakeSchedQualityStore: f.store}
			f.e.store = store
			f.now = f.now.Add(time.Hour)
			var err error
			if rule {
				p := q.DefaultRuleBParams()
				p.Tolerance = .05
				_, err = f.e.UpdateTTFTRule(context.Background(), 0, p)
			} else {
				p := f.store.Policies[10].Policy
				p.TTFTSeconds++
				_, err = f.e.UpdatePolicy(context.Background(), 10, 0, p)
			}
			require.ErrorIs(t, err, errReplaySave)
			require.Zero(t, store.saves)
			require.Equal(t, policies, f.store.Policies)
			require.Zero(t, f.store.Rule.Version)
			require.Equal(t, rows, f.store.Rows)
			require.Equal(t, watermark, f.e.processedAt)
			require.Equal(t, buckets, f.e.peerBuckets)
			require.Same(t, before, f.e.Snapshot())
		})
	}
}

func TestEngineSavePublishesCatchupCountersAndLogs(t *testing.T) {
	f := newQualityFixture(t)
	output := captureSchedObsLogs(t)
	obs := NewSchedObservabilityService()
	f.e.obs = obs
	start := f.now.Add(-10 * time.Second)
	f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: start, Epoch: start, Trial: true, TrialUntil: start.Add(time.Hour), Admissions: 1}}
	f.ledger.Rows = engineSamples(6, start.Add(time.Minute), false)
	for i := range f.ledger.Rows {
		a := &f.ledger.Rows[i]
		a.Outcome, a.Class, a.InsertedAt = q.OutcomeAccountFault, q.ClassUpstream, a.FinishedAt
	}
	f.now = start.Add(40*time.Minute + 10*time.Second)
	_, err := f.e.UpdatePolicy(context.Background(), 10, 0, f.store.Policies[10].Policy)
	require.NoError(t, err)
	require.EqualValues(t, 1, obs.window.cooled)
	require.Contains(t, output.String(), "trial_fail")
	require.Len(t, f.store.Decisions, 1)
	require.Equal(t, start.Add(time.Minute), f.store.Decisions[0].At)
}

func TestEngineUnknownTrialCoalescesArrivals(t *testing.T) {
	f := newQualityFixture(t)
	start := f.now.Add(-50*time.Minute - 10*time.Second)
	horizon := f.now.Add(-10 * time.Second)
	samples := engineSamples(4000, start, false)
	for i := range samples {
		a := &samples[i]
		a.StartedAt = start.Add(time.Duration(i) * time.Microsecond)
		a.FinishedAt = start.Add(time.Second + time.Duration(i)*time.Microsecond)
		a.InsertedAt = a.FinishedAt
		a.Outcome, a.Class = q.OutcomeOther, q.ClassRepairStep
	}
	f.e.window.merge(samples, f.now)
	d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
	require.NoError(t, err)
	state := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: start, Epoch: start, Trial: true, TrialUntil: start.Add(time.Hour)}
	// Keep unrelated lines out of their first regular evaluation during this probe.
	states := []q.LineState{state}
	for _, line := range []q.Dimension{q.DimensionTTFO, q.DimensionCache} {
		r := state
		r.Line, r.Trial, r.EvaluatedAt = line, false, horizon
		states = append(states, r)
	}
	begun := time.Now()
	rows, _, _, decisions, err := f.e.replay(context.Background(), d, states, nil, start, horizon)
	elapsed := time.Since(begun)
	require.NoError(t, err)
	require.Empty(t, decisions)
	for _, r := range rows {
		if r.Line == q.DimensionError {
			require.True(t, r.Trial)
			require.Equal(t, horizon, r.EvaluatedAt)
		}
	}
	require.Equal(t, 51, d.replayStats.assessments, "one arrival grid plus 50 minute clocks")
	require.Equal(t, 51, d.replayStats.batches)
	t.Logf("unknown trial arrivals=4000 pause=50m assessments=%d batches=%d replay=%s", d.replayStats.assessments, d.replayStats.batches, elapsed)
}

func TestEngineArrivalGridDefersBeyondHorizonAndPrecedesTimeout(t *testing.T) {
	f := newQualityFixture(t)
	start := f.now.Add(-10 * time.Second)
	deadline := start.Add(5 * time.Second)
	require.Equal(t, deadline, schedArrivalGrid(deadline))
	require.Equal(t, deadline, schedArrivalGrid(start.Add(time.Microsecond)))
	rows := engineSamples(6, start.Add(4*time.Second), false)
	for i := range rows {
		rows[i].Class, rows[i].Outcome, rows[i].InsertedAt = q.ClassUpstream, q.OutcomeAccountFault, rows[i].FinishedAt
	}
	f.e.window.merge(rows, f.now)
	d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
	require.NoError(t, err)
	state := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: start, Epoch: start, Trial: true, TrialUntil: deadline}
	states, _, _, decisions, err := f.e.replay(context.Background(), d, []q.LineState{state}, nil, start, deadline.Add(-time.Microsecond))
	require.NoError(t, err)
	require.Empty(t, decisions)
	require.Zero(t, d.replayStats.assessments)
	_, _, _, decisions, err = f.e.replay(context.Background(), d, states, nil, start, deadline)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, q.ActionTrialFail, decisions[0].Action, "batched evidence must precede timeout at the same instant")
	require.Equal(t, deadline, decisions[0].At)
}

type movingInsertTail struct {
	*fakeSchedAttemptStore
	through time.Time
	calls   int
}

func (s *movingInsertTail) LoadInsertedAfter(_ context.Context, _ time.Time, limit int) ([]q.AttemptSample, time.Time, error) {
	s.calls++
	if s.calls > 4 {
		return nil, time.Time{}, errors.New("bootstrap kept chasing the moving tail")
	}
	at := s.through.Add(time.Duration(s.calls-2) * time.Second)
	rows := engineSamples(limit+1, at.Add(-time.Second), false)
	for i := range rows {
		rows[i].InsertedAt = at
	}
	return rows, at, nil // A whole equal-timestamp group, larger than the page limit.
}

func TestEngineBootstrapStopsAtFrozenInsertionWatermark(t *testing.T) {
	f := newQualityFixture(t)
	tail := &movingInsertTail{fakeSchedAttemptStore: f.ledger, through: f.now}
	f.e.attempts = tail
	require.NoError(t, f.e.loadEvidence(context.Background(), f.now))
	require.True(t, f.e.loaded)
	require.Equal(t, 2, tail.calls)
	require.Equal(t, f.now, f.e.loadThrough)
	require.Equal(t, f.now, f.e.insertedAt)
	require.Len(t, f.e.window.samples[SchedAccountModel{1, "synthetic-model"}], 2*(schedQualityLedgerPageSize+1))
}
