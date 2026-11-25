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

// Acceptance 6: raw hour-wide evidence, line eligibility, visibility and uncertainty.
func TestRecoveryOwnSampleAt60(t *testing.T) {
	f := newQualityFixture(t)
	origin := f.now
	check := origin.Add(time.Hour)
	r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model"}, Epoch: origin}
	for _, line := range q.Lines {
		t.Run(string(line), func(t *testing.T) {
			r.Line = line
			sample := engineSamples(1, origin.Add(time.Minute), true)[0]
			sample.GroupID = 99 // Other group, and outside the current cache statistics window.
			sample.SessionKey = "synthetic-session"
			sample.Continuation = true
			sample.InputTokens = new(1)
			sample.CacheReadTokens = new(99)
			sample.CacheCreationTokens = new(0)
			for _, tc := range []struct {
				name string
				edit func(*q.AttemptSample)
				want bool
			}{
				{"other-group", func(*q.AttemptSample) {}, true},
				{"boundary-start", func(a *q.AttemptSample) { a.StartedAt = origin }, false},
				{"started-before", func(a *q.AttemptSample) { a.StartedAt = origin.Add(-time.Second) }, false},
				{"late-insertion", func(a *q.AttemptSample) { a.InsertedAt = check.Add(time.Second) }, false},
				{"inflight-any-type", func(a *q.AttemptSample) {
					a.FinishedAt = check.Add(time.Minute)
					a.Stream = false
					a.Continuation = false
				}, true},
				{"not-eligible", func(a *q.AttemptSample) {
					a.Stream = false
					a.Continuation = false
					a.Outcome = q.OutcomeAccountFault
					a.Class = q.ClassClientCancel
				}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					a := sample
					tc.edit(&a)
					d := &schedQualityData{observedSince: origin.Add(-time.Second), evidenceSince: origin.Add(-time.Hour), samples: map[SchedAccountModel][]q.AttemptSample{{1, r.Model}: {a}}}
					require.Equal(t, new(tc.want), schedTrialOwnSample(d, r, check))
				})
			}
			d := &schedQualityData{observedSince: origin.Add(time.Second)}
			require.Nil(t, schedTrialOwnSample(d, r, check), "lost inflight history is not zero")
			d.observedSince = origin.Add(-time.Second)
			d.running = map[string]q.AttemptSample{"inflight": {AccountID: 1, Model: r.Model, StartedAt: origin.Add(time.Minute)}}
			require.Equal(t, new(true), schedTrialOwnSample(d, r, check))
		})
	}
	r.Line = q.DimensionTTFO
	a := engineSamples(1, origin.Add(time.Minute), true)[0]
	a.TTFOMs = nil
	a.Outcome = q.OutcomeTimeout
	d := &schedQualityData{samples: map[SchedAccountModel][]q.AttemptSample{{1, r.Model}: {a}}}
	require.Equal(t, new(true), schedTrialOwnSample(d, r, check), "first-output timeout is usable")
}

// Acceptance 1/5/7: off-minute phases execute once, rounds stay immutable,
// passing trial evidence is recorded but normal identity starts unknown.
func TestRecoveryOffMinutePhases(t *testing.T) {
	for _, scenario := range []string{"pass15", "empty60", "sample60", "old60"} {
		t.Run(scenario, func(t *testing.T) {
			f := newQualityFixture(t)
			origin := f.now.Truncate(time.Minute).Add(5 * time.Second)
			offset := time.Hour
			if scenario == "pass15" {
				offset = q.TrialMinimum
			}
			at := origin.Add(offset)
			key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}
			state := q.LineState{LineKey: key, Trial: true, Epoch: origin, TrialUntil: origin.Add(q.TrialDuration), EvaluatedAt: at.Add(-time.Second), Admissions: 1, LastRear: origin.Add(-20 * time.Minute)}
			if scenario == "old60" {
				state.TrialUntil = at
			}
			f.e.observedSince = origin.Add(-time.Second)
			f.now = at.Add(10 * time.Second)
			f.store.Rows = []q.LineState{state}
			switch scenario {
			case "pass15":
				f.ledger.Rows = errorRecoverySamples(52, 0, at.Add(-time.Minute))
			case "sample60", "old60":
				f.ledger.Rows = engineSamples(1, origin.Add(time.Minute), false)
			}
			f.cycle()
			got := f.line(q.DimensionError)
			if scenario == "sample60" {
				require.True(t, got.Trial)
				require.Equal(t, state.TrialUntil, got.TrialUntil)
				require.Empty(t, f.store.Decisions)
			} else {
				require.False(t, got.Trial)
				require.Equal(t, q.IdentityUnknown, got.Identity)
				require.Equal(t, at, got.Epoch)
				require.Len(t, f.store.Decisions, 1)
				decision := f.store.Decisions[0]
				require.Equal(t, at, decision.At)
				want := "no_samples"
				if scenario == "pass15" {
					want = "qualified"
					require.Equal(t, q.ReasonWithin, got.LastAssessment[len(got.LastAssessment)-1].Reason)
				}
				if scenario == "old60" {
					want = "deadline"
				}
				require.Equal(t, want, decision.Basis.ReleaseReason)
			}
			before := len(f.store.Decisions)
			f.now = f.now.Add(5 * time.Second)
			f.cycle()
			require.Equal(t, before, len(f.store.Decisions))
			require.Equal(t, got.EvaluatedAt, f.line(q.DimensionError).EvaluatedAt, "elapsed checkpoints must not be reposted five seconds later")
		})
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
				require.Equal(t, origin.Add(360*time.Minute), r.TrialUntil)
			} else {
				require.False(t, r.Trial)
				require.Equal(t, origin.Add(360*time.Minute), r.Epoch)
				require.Equal(t, origin.Add(360*time.Minute), f.store.Decisions[1].At)
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
		cache := engineSamples(20, at.Add(-2*time.Second), false)
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
				require.Equal(t, 2, r.Level)
				require.Equal(t, at.Add(80*time.Minute), r.Until)
			}
		}
		require.Len(t, decisions, 3)
	}
}

// Acceptance 3: relaxing one line preserves the remaining group's recurrence history.
func TestRecoveryRelaxUsesRemainingHistory(t *testing.T) {
	for _, history := range []bool{false, true} {
		f := newQualityFixture(t)
		f.badTTFT()
		f.cycle()
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
		p := q.DefaultRuleBParams()
		p.Confidence = .9
		_, err := f.e.UpdateTTFTRule(context.Background(), 0, p)
		require.NoError(t, err)
		want := 0
		if history {
			want = 3
		}
		require.Equal(t, want, f.line(q.DimensionTTFO).Level)
	}
}

func TestRecoveryOwnSampleHonorsRequestOrigin(t *testing.T) {
	f := newQualityFixture(t)
	origin := f.now
	check := origin.Add(time.Hour)
	first := engineSamples(1, origin.Add(-time.Second), false)[0]
	retry := first
	retry.AttemptID = "retry"
	retry.StartedAt = origin.Add(time.Minute)
	retry.FinishedAt = origin.Add(2 * time.Minute)
	d := &schedQualityData{observedSince: origin.Add(-time.Second), samples: map[SchedAccountModel][]q.AttemptSample{{1, first.Model}: {first, retry}}}
	r := q.LineState{LineKey: q.LineKey{AccountID: 1, Model: first.Model, Line: q.DimensionError}, Epoch: origin}
	require.Equal(t, new(false), schedTrialOwnSample(d, r, check))
}

// Acceptance 2/4: staggered persisted watermarks must not spin on an overdue clear.
func TestRecoveryQuietClearStaggeredWatermarks(t *testing.T) {
	f := newQualityFixture(t)
	horizon := f.now.Add(-10 * time.Second)
	deadline := horizon.Add(-2 * time.Hour)
	var states []q.LineState
	for i, line := range q.Lines {
		watermark := deadline.Add(-time.Hour)
		if i == 0 {
			watermark = deadline.Add(time.Hour)
		}
		states = append(states, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: line}, Level: 2, Admissions: 1, LastRear: deadline.Add(-q.LevelClearQuiet), EvaluatedAt: watermark})
	}
	d, err := f.e.inputs(context.Background(), f.now, nil)
	require.NoError(t, err)
	rows, _, _, decisions, err := f.e.replay(context.Background(), d, states, nil, time.Time{}, horizon)
	require.NoError(t, err)
	for _, r := range rows {
		require.Zero(t, r.Admissions)
		require.Zero(t, r.Level)
	}
	require.Len(t, decisions, 3)
	require.Less(t, d.replayStats.batches, 60)
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
	held, action := q.Step(trial, q.EventTrial, q.VerdictUnknown, q.Basis{OwnSampleAt60: new(true)}, origin.Add(time.Hour))
	require.Empty(t, action)
	require.Equal(t, trial.TrialUntil, held.TrialUntil)
	f.svc.selectionBoard.now = func() time.Time { return origin.Add(70 * time.Minute) }
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
	origin := at.Add(-130 * time.Minute)
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

// Acceptance 6: quality's temporary predecessor mask also applies to the
// original-hour sample check, including a completion still waiting for insertion.
func TestRecoveryOwnCacheSampleUsesPredecessorMask(t *testing.T) {
	f := newQualityFixture(t)
	origin := f.now
	check := origin.Add(time.Hour)
	current := engineSamples(1, origin.Add(time.Minute), false)[0]
	current.SessionKey = "session"
	current.Continuation = true
	current.InputTokens = new(1)
	current.CacheReadTokens = new(99)
	current.CacheCreationTokens = new(0)
	for _, finished := range []bool{false, true} {
		previous := current
		previous.AttemptID = "unmerged"
		previous.StartedAt = origin.Add(-time.Minute)
		previous.FinishedAt = time.Time{}
		if finished {
			previous.FinishedAt = origin.Add(30 * time.Second)
		}
		d := &schedQualityData{observedSince: origin.Add(-time.Hour), samples: map[SchedAccountModel][]q.AttemptSample{{1, current.Model}: {current}}, running: map[string]q.AttemptSample{previous.AttemptID: previous}}
		r := q.LineState{LineKey: q.LineKey{AccountID: 1, Model: current.Model, Line: q.DimensionCache}, Epoch: origin}
		require.Equal(t, new(false), schedTrialOwnSample(d, r, check))
		require.True(t, d.samples[SchedAccountModel{1, current.Model}][0].Continuation, "raw evidence remains immutable")
	}
}
