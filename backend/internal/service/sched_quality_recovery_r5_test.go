//go:build unit

package service

import (
	"context"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"sort"
	"testing"
	"time"
)

// Acceptance 1/5/7: off-minute phases execute once, rounds stay immutable,
// passing trial evidence is recorded but normal identity starts unknown.
func TestRecoveryOffMinutePhases(t *testing.T) {
	for _, offset := range []time.Duration{q.TrialDuration - time.Second, q.TrialDuration} {
		f := newQualityFixture(t)
		origin := f.now.Add(-5 * time.Second)
		f.now = origin.Add(offset + 10*time.Second)
		key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}
		f.store.Rows = []q.LineState{{LineKey: key, Trial: true, Epoch: origin, TrialUntil: origin.Add(q.TrialDuration), EvaluatedAt: origin}}
		f.cycle()
		if offset < q.TrialDuration {
			require.True(t, f.line(q.DimensionError).Trial)
			require.Empty(t, f.store.Decisions)
		} else {
			require.False(t, f.line(q.DimensionError).Trial)
			require.Len(t, f.store.Decisions, 1)
			require.Equal(t, origin.Add(q.TrialDuration), f.store.Decisions[0].At)
			require.Equal(t, "deadline", f.store.Decisions[0].Basis.ReleaseReason)
		}
	}
}

// Acceptance 2: retained history cannot move original deadlines after restart.
func TestRecoveryRestartOriginalDeadlines(t *testing.T) {
	for _, minute := range []int{250, 500} {
		t.Run(time.Duration(minute).String(), func(t *testing.T) {
			f := newQualityFixture(t)
			origin := f.now.Truncate(time.Minute)
			f.now = origin.Add(time.Duration(minute)*time.Minute + 10*time.Second)
			f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
			f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, Identity: q.IdentityRear, Level: 4, Admissions: 1, Entry: origin, Epoch: origin, LastRear: origin, EvaluatedAt: origin, Until: origin.Add(240 * time.Minute)}}
			f.cycle()
			r := f.line(q.DimensionError)
			require.Equal(t, origin.Add(240*time.Minute), f.store.Decisions[0].At)
			if minute == 250 {
				require.True(t, r.Trial)
				require.Equal(t, origin.Add(300*time.Minute), r.TrialUntil)
			} else {
				require.False(t, r.Trial)
				require.Equal(t, origin.Add(300*time.Minute), r.Epoch)
				require.Equal(t, origin.Add(300*time.Minute), f.store.Decisions[1].At)
				require.True(t, f.store.Decisions[0].Basis.HistoryMissing)
			}
			before := len(f.store.Decisions)
			require.NoError(t, f.e.cycle(context.Background()))
			require.Len(t, f.store.Decisions, before)
		})
	}
}

// Acceptance 3: production replay freezes the group before applying mixed exits.
func TestRecoveryReplayMixedPermutations(t *testing.T) {
	for _, order := range [][]q.Dimension{{q.DimensionTTFO, q.DimensionError, q.DimensionCache}, {q.DimensionTTFO, q.DimensionCache, q.DimensionError}, {q.DimensionError, q.DimensionTTFO, q.DimensionCache}, {q.DimensionError, q.DimensionCache, q.DimensionTTFO}, {q.DimensionCache, q.DimensionTTFO, q.DimensionError}, {q.DimensionCache, q.DimensionError, q.DimensionTTFO}} {
		f := newQualityFixture(t)
		f.badTTFT()
		at := f.now.Add(-10 * time.Second)
		samples := append([]q.AttemptSample{}, f.ledger.Rows...)
		failures := errorRecoverySamples(20, 20, at.Add(-time.Second))
		for i := range failures {
			failures[i].RequestID = "bad-" + failures[i].AttemptID
		}
		samples = append(samples, failures...)
		cache := engineSamples(40, at.Add(-2*time.Second), false)
		for i := range cache {
			cache[i].RequestID = "cache-" + cache[i].AttemptID
			cache[i].SessionKey = "cache-session"
			cache[i].Continuation = true
			cache[i].InputTokens = new(1)
			cache[i].CacheReadTokens = new(99)
			cache[i].CacheCreationTokens = new(0)
		}
		samples = append(samples, cache...)
		byPair := map[SchedAccountModel][]q.AttemptSample{}
		for _, a := range samples {
			p := SchedAccountModel{a.AccountID, a.Model}
			byPair[p] = append(byPair[p], a)
		}
		d, err := f.e.inputs(context.Background(), f.now, byPair)
		require.NoError(t, err)
		d.ruleGroups = map[schedGroupModel][]q.RuleBSample{{10, "synthetic-model"}: ruleBSamples(samples)}
		var states []q.LineState
		for _, line := range q.Lines {
			r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: line}, EvaluatedAt: at.Add(-time.Second)}
			switch line {
			case q.DimensionTTFO:
				r.Identity = q.IdentityRear
				r.Level = 1
				r.Admissions = 1
				r.Entry = at.Add(-40 * time.Minute)
				r.Until = at
			case q.DimensionError:
				r.PendingAt = at.Add(-q.RegularInterval)
			case q.DimensionCache:
				r.Trial = true
				r.Epoch = at.Add(-30 * time.Minute)
				r.TrialUntil = at.Add(90 * time.Minute)
			}
			states = append(states, r)
		}
		result, _, _, decisions, err := f.e.replay(context.Background(), d, states, nil, at.Add(-time.Second), at, func(keys []q.LineKey) {
			rank := map[q.Dimension]int{}
			for i, l := range order {
				rank[l] = i
			}
			sort.SliceStable(keys, func(i, j int) bool { return rank[keys[i].Line] < rank[keys[j].Line] })
		})
		require.NoError(t, err)
		for _, r := range result {
			if r.AccountID != 1 {
				continue
			}
			if r.Line == q.DimensionCache {
				require.False(t, r.Trial)
				require.Equal(t, q.IdentityUnknown, r.Identity)
				require.Equal(t, at, r.Epoch)
			} else {
				require.Equal(t, q.IdentityRear, r.Identity)
				require.Zero(t, r.Level)
				require.Equal(t, at.Add(q.RearDuration), r.Until)
			}
		}
		require.Len(t, decisions, 3)
	}
}

// Acceptance 3: relaxing one line does not use another line's historical failures.
func TestRecoveryRelaxDoesNotUseSiblingHistory(t *testing.T) {
	for _, history := range []bool{false, true} {
		f := newQualityFixture(t)
		f.badTTFT()
		for i := range f.ledger.Rows {
			if f.ledger.Rows[i].AccountID == 1 {
				f.ledger.Rows[i].TTFOMs = new(100000)
			}
		}
		f.cycle()
		require.Equal(t, q.IdentityRear, f.line(q.DimensionTTFO).Identity)
		if history {
			for i := range f.store.Rows {
				r := &f.store.Rows[i]
				if r.AccountID == 1 && r.Line == q.DimensionError {
					r.Level = 2
					r.Admissions = 1
					r.LastRear = f.now
				}
			}
		}
		sibling := f.line(q.DimensionError)
		oldDecisions := len(f.store.Decisions)
		p := q.DefaultRuleBParams()
		p.Confidence = .9
		_, err := f.e.UpdateTTFTRule(context.Background(), 0, p)
		require.NoError(t, err)
		entered := f.line(q.DimensionTTFO)
		require.Equal(t, q.IdentityRear, entered.Identity)
		require.False(t, entered.Trial)
		require.Zero(t, entered.Level)
		require.Equal(t, 1, entered.Admissions)
		require.Equal(t, f.now, entered.Entry)
		require.Equal(t, f.now, entered.Epoch)
		require.Equal(t, f.now.Add(60*time.Minute), entered.Until)
		require.Equal(t, sibling, f.line(q.DimensionError))
		var actions []q.Action
		for _, decision := range f.store.Decisions[oldDecisions:] {
			if decision.Key == entered.LineKey {
				actions = append(actions, decision.Action)
				require.Equal(t, f.now, decision.At)
			}
		}
		require.Equal(t, []q.Action{q.ActionRelax, q.ActionEnter}, actions)
	}
}

// Acceptance 7: a reservation from the new 120-minute round still promotes
// after the old one-hour duration, using the unchanged authoritative round check.
func TestRecoveryTrialRoundLateBinding(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	origin := f.view.At()
	rear := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 7, Model: "m", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Until: origin, Admissions: 1}
	trial, action := q.Step(rear, q.EventReview, q.VerdictUnknown, q.Basis{}, origin)
	require.Equal(t, q.ActionTrial, action)
	key := fakeSchedViewKey{7, 1, "m"}
	f.view.States[key] = q.StateView{TTFO: q.LineView{Trial: true, TrialUntil: trial.TrialUntil}}
	db := &trialRoundStoreProbe{fakeSchedView: f.view, current: f.view.States[key]}
	f.svc.selectionBoard.view = func() q.View { return db }
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := selectTrialRuntime(t, f, ctx, "session")
	held, action := q.Step(trial, q.EventTrial, q.VerdictUnknown, q.Basis{}, origin.Add(time.Hour-time.Second))
	require.Empty(t, action)
	require.Equal(t, trial.TrialUntil, held.TrialUntil)
	f.svc.selectionBoard.now = func() time.Time { return origin.Add(59 * time.Minute) }
	CompleteOpenAIStickyChange(ctx, 1, true, false)
	selected.ReleaseFunc()
	require.Equal(t, 1, db.reads)
	require.True(t, leases.held[trialTestKey(1, "m", 7, "s:session")])
}

// Acceptance 2/9: missing deadlines are repaired at read time; anomaly and
// incomplete-history logs appear only once the transaction commits.
func TestRecoveryDiagnosticLogsAfterCommit(t *testing.T) {
	output := captureSchedObsLogs(t)
	f := newQualityFixture(t)
	at := f.now.Add(-10 * time.Second)
	origin := at.Add(-6 * time.Hour)
	f.store.Rows = []q.LineState{
		{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Trial: true, Epoch: origin, TrialUntil: origin.Add(120 * time.Minute), EvaluatedAt: origin},
		{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionCache}, Trial: true, Epoch: origin, EvaluatedAt: origin},
	}
	f.store.CommitErr = context.Canceled
	require.ErrorIs(t, f.e.cycle(context.Background()), context.Canceled)
	for _, r := range schedObsLogRecords(t, output) {
		require.NotEqual(t, "sched_quality.history_missing", r["msg"])
		require.NotEqual(t, "sched_quality.missing_deadline", r["msg"])
		require.NotEqual(t, "sched_quality.state", r["msg"])
	}
	output.Reset()
	f.store.CommitErr = nil
	f.cycle()
	counts := map[string]int{}
	for _, r := range schedObsLogRecords(t, output) {
		msg, _ := r["msg"].(string)
		counts[msg]++
		if msg == "sched_quality.state" {
			require.Equal(t, "deadline", r["release_reason"])
			require.Equal(t, q.RuleVersion, r["rule_version"])
			require.Greater(t, r["trial_duration_seconds"].(float64), float64(0))
		}
	}
	require.Greater(t, counts["sched_quality.history_missing"], 0)
	require.Equal(t, 1, counts["sched_quality.missing_deadline"])
	for _, d := range f.store.Decisions {
		if d.Key.Line == q.DimensionCache {
			require.Equal(t, at, d.At)
			require.True(t, d.Basis.MissingDeadline)
		}
	}
	output.Reset()
	f.cycle() // Publish the committed state again at the same horizon.
	for _, r := range schedObsLogRecords(t, output) {
		require.NotEqual(t, "sched_quality.missing_deadline", r["msg"])
		require.NotEqual(t, "sched_quality.history_missing", r["msg"])
	}
}
