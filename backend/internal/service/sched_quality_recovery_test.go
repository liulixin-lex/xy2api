//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func errorRecoverySamples(clear, failed int, at time.Time) []q.AttemptSample {
	rows := engineSamples(clear, at, false)
	for i := range rows {
		rows[i].RequestID = rows[i].AttemptID
		rows[i].InsertedAt = at
		if i < failed {
			rows[i].Outcome = q.OutcomeAccountFault
			rows[i].Class = q.ClassUpstream
		}
	}
	return rows
}

func TestEngineErrorRecoveryByEvent(t *testing.T) {
	for _, event := range []struct {
		name string
		kind q.EventKind
		want q.Verdict
	}{
		{"regular", q.EventRegular, q.VerdictPass},
		{"review", q.EventReview, q.VerdictUnknown},
		{"trial", q.EventTrial, q.VerdictUnknown},
	} {
		t.Run(event.name, func(t *testing.T) {
			f := newQualityFixture(t)
			f.e.policies = f.store.Policies
			key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}
			pair := SchedAccountModel{key.AccountID, key.Model}
			row := q.LineState{LineKey: key}
			for _, sample := range []struct {
				failed int
				want   q.Verdict
			}{
				{5, event.want},
				{6, q.VerdictFail},
			} {
				d := &schedQualityData{samples: map[SchedAccountModel][]q.AttemptSample{
					pair: errorRecoverySamples(52, sample.failed, f.now.Add(-time.Second)),
				}}
				verdict, basis := f.e.assessLine(d, row, event.kind, f.now, nil)
				require.Equal(t, sample.want, verdict)
				require.NotNil(t, basis.Error)
				require.Equal(t, verdict, basis.Error.Verdict)
				require.Equal(t, 52, basis.Error.Clear)
				require.Equal(t, sample.failed, basis.Error.Failed)

				raw, err := json.Marshal(basis)
				require.NoError(t, err)
				var stored q.Basis
				require.NoError(t, json.Unmarshal(raw, &stored))
				require.NotNil(t, stored.Error)
				require.Equal(t, verdict, stored.Error.Verdict)
				if sample.want == q.VerdictUnknown {
					require.True(t, stored.Error.RecoveryOverLimit)
					require.Contains(t, string(raw), `"recovery_over_limit":true`)
				} else {
					require.False(t, stored.Error.RecoveryOverLimit)
					require.NotContains(t, string(raw), "recovery_over_limit")
				}
			}
		})
	}
}

func TestEngineErrorRecoveryKeepsTrialAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   q.Action
		identity q.Identity
		verdict  q.Verdict
	}{
		{"later-failure", q.ActionTrialFail, q.IdentityRear, q.VerdictFail},
		{"still-over-limit-at-deadline", q.ActionTrialTimeout, q.IdentityUnknown, q.VerdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQualityFixture(t)
			reviewAt := f.now.Add(-10 * time.Second)
			entry := reviewAt.Add(-q.CooldownLadder[0])
			f.store.Rows = []q.LineState{{
				LineKey:  q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError},
				Identity: q.IdentityRear, Entry: entry, LastRear: entry, Until: reviewAt, EvaluatedAt: entry, Admissions: 1,
			}}
			f.ledger.Rows = errorRecoverySamples(52, 5, reviewAt.Add(-30*time.Second))
			f.cycle()
			require.Equal(t, q.PlaceTrial, f.view().Placement(true, f.now))
			require.Len(t, f.store.Decisions, 1)
			review := f.store.Decisions[0]
			require.Equal(t, q.ActionTrial, review.Action)
			require.Equal(t, "review", review.Basis.Kind)
			require.NotNil(t, review.Basis.Error)
			require.Equal(t, q.VerdictUnknown, review.Basis.Error.Verdict)
			require.True(t, review.Basis.Error.RecoveryOverLimit)
			require.Equal(t, &review.Basis, f.line(q.DimensionError).Basis)

			trialAt := reviewAt.Add(time.Minute)
			f.now = trialAt.Add(10 * time.Second)
			f.ledger.Rows = append(f.ledger.Rows, errorRecoverySamples(52, 5, trialAt)...)
			f.cycle()
			trial := f.line(q.DimensionError)
			require.Equal(t, trialAt, trial.EvaluatedAt)
			require.Equal(t, reviewAt.Add(q.TrialDuration), trial.TrialUntil)
			require.Equal(t, q.PlaceTrial, f.view().Placement(true, f.now))
			require.Len(t, f.store.Decisions, 1)

			f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
			f.cycle()
			require.Equal(t, trial, f.line(q.DimensionError))
			require.Equal(t, q.PlaceTrial, f.view().Placement(true, f.now))
			require.Len(t, f.store.Decisions, 1)

			endAt := trial.TrialUntil
			if tc.action == q.ActionTrialFail {
				endAt = trialAt.Add(time.Minute)
				f.ledger.Rows = append(f.ledger.Rows, errorRecoverySamples(1, 1, endAt)...)
			}
			f.now = endAt.Add(10 * time.Second)
			f.cycle()
			state := f.line(q.DimensionError)
			require.False(t, state.Trial)
			require.Equal(t, tc.identity, state.Identity)
			require.Len(t, f.store.Decisions, 2)
			decision := f.store.Decisions[1]
			require.Equal(t, endAt, decision.At)
			require.Equal(t, tc.action, decision.Action)
			require.Equal(t, "trial", decision.Basis.Kind)
			require.NotNil(t, decision.Basis.Error)
			require.Equal(t, tc.verdict, decision.Basis.Error.Verdict)
			require.Equal(t, tc.verdict == q.VerdictUnknown, decision.Basis.Error.RecoveryOverLimit)
			require.Equal(t, &decision.Basis, state.Basis)
		})
	}
}

func TestEngineRestartRecoversTrialArrivalsAndMinutes(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	run := func(mode string) q.LineState {
		f := newQualityFixture(t)
		f.now = start.Add(50*time.Minute + 10*time.Second)
		f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, Trial: true, Epoch: start, TrialUntil: start.Add(time.Hour), Admissions: 1, LastRear: start.Add(-20 * time.Minute), EvaluatedAt: start}}
		f.ledger.Rows = engineSamples(6, start.Add(time.Minute), false)
		for i := range f.ledger.Rows {
			r := &f.ledger.Rows[i]
			r.Outcome = q.OutcomeAccountFault
			r.Class = q.ClassUpstream
			r.InsertedAt = r.FinishedAt
		}
		if mode != "restart" {
			f.e.processedAt = start
		}
		if mode == "continuous" {
			f.now = start.Add(time.Minute + 10*time.Second)
			f.cycle()
			require.Equal(t, start.Add(time.Minute), f.line(q.DimensionError).Entry)
			f.now = start.Add(50*time.Minute + 10*time.Second)
		}
		f.cycle()
		state := f.line(q.DimensionError)
		require.True(t, state.Trial)
		require.Equal(t, start.Add(time.Minute), state.Entry)
		require.Equal(t, start.Add(41*time.Minute), state.Epoch)
		require.Len(t, f.store.Decisions, 2)
		require.Equal(t, q.ActionTrialFail, f.store.Decisions[0].Action)
		require.Equal(t, start.Add(time.Minute), f.store.Decisions[0].At)
		require.Equal(t, q.ActionTrial, f.store.Decisions[1].Action)
		require.Equal(t, start.Add(41*time.Minute), f.store.Decisions[1].At)
		f.e.processedAt = time.Time{}
		f.cycle()
		require.Equal(t, state, f.line(q.DimensionError))
		require.Len(t, f.store.Decisions, 2)
		return state
	}
	want := run("continuous")
	require.Equal(t, want, run("pause"))
	require.Equal(t, want, run("restart"))
}

func TestEngineNewKeysDoNotInheritOlderKeyReplayStart(t *testing.T) {
	f := newQualityFixture(t)
	at := f.now.Add(-10 * time.Second)
	start := at.Add(-50 * time.Minute)
	f.store.Members[10] = []int64{1, 2}
	f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: start}}
	f.ledger.Rows = engineSamples(6, start.Add(time.Minute), false)
	for i := range f.ledger.Rows {
		r := &f.ledger.Rows[i]
		r.AccountID = 2
		r.Outcome = q.OutcomeAccountFault
		r.Class = q.ClassUpstream
		r.InsertedAt = r.FinishedAt
	}
	f.cycle()
	for _, d := range f.store.Decisions {
		require.NotEqual(t, int64(2), d.Key.AccountID, "a newly discovered key must not inherit historical confirmation")
	}
	require.Equal(t, q.IdentityUnknown, f.e.Snapshot().View.StateOf(10, 2, "synthetic-model").Error.Identity)
	for _, r := range f.store.Rows {
		if r.AccountID == 2 && r.Line == q.DimensionError {
			require.Equal(t, at, r.PendingAt)
		}
	}
}

type failingEvidencePages struct {
	*fakeSchedAttemptStore
	cursors []SchedAttemptCursor
	failed  bool
}

func (p *failingEvidencePages) LoadFinishedSince(context.Context, time.Time) ([]q.AttemptSample, error) {
	panic("engine must use bounded pages")
}
func (p *failingEvidencePages) LoadFinishedPage(ctx context.Context, since, through time.Time, after SchedAttemptCursor, limit int) ([]q.AttemptSample, error) {
	p.cursors = append(p.cursors, after)
	if len(p.cursors) == 2 && !p.failed {
		p.failed = true
		return nil, context.DeadlineExceeded
	}
	return p.fakeSchedAttemptStore.LoadFinishedPage(ctx, since, through, after, limit)
}
func TestEngineStartupPagesResumeAfterFailure(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = engineSamples(schedQualityLedgerPageSize+1, f.now.Add(-time.Minute), false)
	pages := &failingEvidencePages{fakeSchedAttemptStore: f.ledger}
	f.e.attempts = pages
	initial := f.e.Snapshot()
	require.ErrorIs(t, f.e.cycle(context.Background()), context.DeadlineExceeded)
	require.False(t, f.e.loaded)
	require.Same(t, initial, f.e.Snapshot())
	require.False(t, f.e.Snapshot().GlobalEnabled, "partial evidence must not enable layering")
	require.Len(t, f.e.window.samples[SchedAccountModel{1, "synthetic-model"}], schedQualityLedgerPageSize)
	require.NoError(t, f.e.SetOverride(context.Background(), SchedOverride{AccountID: 1, GroupID: 10, Mode: q.OverrideExempt}))
	require.False(t, f.e.Snapshot().View.GlobalEnabled(), "an administrative rebuild cannot enable partial evidence")
	checkpoint := f.e.finishedCursor
	require.NotEmpty(t, checkpoint.AttemptID)
	f.cycle()
	require.True(t, f.e.loaded)
	require.Len(t, pages.cursors, 3)
	require.Equal(t, checkpoint, pages.cursors[2], "resume the failed page without repeating the full window")
	require.Len(t, f.e.window.samples[SchedAccountModel{1, "synthetic-model"}], schedQualityLedgerPageSize+1)
}

type replayCancelContext struct {
	context.Context
	remaining int
}

func (c *replayCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
func TestEngineReplayChecksCancellation(t *testing.T) {
	f := newQualityFixture(t)
	at := f.now.Add(-10 * time.Second)
	state := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: at.Add(-24 * time.Hour)}
	d, err := f.e.inputs(context.Background(), f.now, nil)
	require.NoError(t, err)
	ctx := &replayCancelContext{Context: context.Background(), remaining: 8}
	_, _, _, _, err = f.e.replay(ctx, d, []q.LineState{state}, nil, at.Add(-time.Minute), at)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, f.store.Rows)
}

func TestEngineDistinctArrivalCatchupTiming(t *testing.T) {
	for _, n := range []int{2000, 4000, 8000, 16000} {
		for _, trial := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/trial=%v", n, trial), func(t *testing.T) {
				f := newQualityFixture(t)
				start := f.now.Add(-50*time.Minute - 10*time.Second)
				at := f.now.Add(-10 * time.Second)
				samples := engineSamples(n, start, false)
				for i := range samples {
					r := &samples[i]
					r.FinishedAt = start.Add(time.Second + time.Duration(i)*time.Microsecond)
					r.InsertedAt = r.FinishedAt
					r.StartedAt = start.Add(time.Duration(i) * time.Microsecond)
					r.Outcome = q.OutcomeOther
					r.Class = q.ClassRepairStep
					if i < 100 {
						r.Outcome = q.OutcomeAccountFault
						r.Class = q.ClassUpstream
					}
				}
				f.e.window.merge(samples, f.now)
				d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
				require.NoError(t, err)
				state := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, EvaluatedAt: start, Epoch: start.Add(-time.Second), Trial: trial, TrialUntil: start.Add(time.Hour), Admissions: 1, LastRear: start.Add(-20 * time.Minute)}
				begun := time.Now()
				rows, _, _, decisions, err := f.e.replay(context.Background(), d, []q.LineState{state}, nil, start, at)
				elapsed := time.Since(begun)
				require.NoError(t, err)
				require.NotEmpty(t, rows)
				if trial {
					require.NotEmpty(t, decisions)
					require.Equal(t, q.ActionTrialFail, decisions[0].Action)
					require.True(t, decisions[0].At.Before(start.Add(time.Minute)))
				}
				t.Logf("distinct microsecond arrivals=%d pause=50m trial=%v replay=%s", n, trial, elapsed)
			})
		}
	}
}

// These two inherited limitations are characterized, not silently "fixed":
// inserted_at clock reversal and per-attempt switch exclusion (R6-B cold==2).
func TestEngineKnownClockBackstepAndMovedRoundRetry(t *testing.T) {
	t.Run("database_clock_backstep", func(t *testing.T) {
		f := newQualityFixture(t)
		old := engineSamples(1, f.now.Add(-10*time.Second), false)[0]
		old.InsertedAt = old.FinishedAt
		f.ledger.Rows = []q.AttemptSample{old}
		f.cycle()
		late := old
		late.AttemptID = "backstep"
		late.InsertedAt = old.InsertedAt.Add(-time.Minute)
		f.ledger.Rows = append(f.ledger.Rows, late)
		f.now = f.now.Add(5 * time.Second)
		f.cycle()
		require.Len(t, f.e.window.samples[SchedAccountModel{1, old.Model}], 1)
		t.Log("Known limitation: insertion-clock reversal beyond overlap is not discovered until reload")
	})
	t.Run("retry_first_moved_round", func(t *testing.T) {
		f := newQualityFixture(t)
		rows := engineSamples(3, f.now.Add(-time.Minute), true)
		for i := range rows {
			r := &rows[i]
			r.SessionKey = "moved"
			r.StartedAt = r.StartedAt.Add(time.Duration(i) * 10 * time.Second)
			r.FinishedAt = r.FinishedAt.Add(time.Duration(i) * 10 * time.Second)
			r.InsertedAt = r.FinishedAt
		}
		rows[0].AccountID = 2
		rows[0].RequestID = "previous"
		rows[1].RequestID = "moved-round"
		rows[2].RequestID = "moved-round"
		f.ledger.Rows = rows
		f.cycle()
		own := f.e.window.samples[SchedAccountModel{1, "synthetic-model"}]
		require.True(t, own[0].Switched)
		require.False(t, own[1].Switched)
		count := q.CountRuleBEvidence(ruleBSamples(own), q.RuleBScope{AccountID: 1, GroupID: 10, Model: "synthetic-model"}, f.now, time.Time{}, 1)
		require.Greater(t, count.N, 0.0)
		require.False(t, f.e.Snapshot().View.SessionSlowTwice(10, 1, "synthetic-model", "moved"))
	})
}

type forbidConfigReadsInTx struct {
	*fakeSchedQualityStore
	inside bool
}

func (s *forbidConfigReadsInTx) LoadPolicies(ctx context.Context) (map[int64]SchedPolicyRecord, error) {
	if s.inside {
		return nil, errors.New("nested policy connection")
	}
	return s.fakeSchedQualityStore.LoadPolicies(ctx)
}
func (s *forbidConfigReadsInTx) LoadTTFTRule(ctx context.Context) (SchedTTFTRuleRecord, error) {
	if s.inside {
		return SchedTTFTRuleRecord{}, errors.New("nested TTFT connection")
	}
	return s.fakeSchedQualityStore.LoadTTFTRule(ctx)
}
func (s *forbidConfigReadsInTx) RunCycle(ctx context.Context, fn func(SchedQualityTx) error) (bool, error) {
	return s.fakeSchedQualityStore.RunCycle(ctx, func(tx SchedQualityTx) error { s.inside = true; defer func() { s.inside = false }(); return fn(tx) })
}
func (s *forbidConfigReadsInTx) RunExclusive(ctx context.Context, fn func(SchedQualityTx) error) error {
	_, err := s.RunCycle(ctx, fn)
	return err
}
func TestEngineConfigurationReadsStayInsideTransaction(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.e.store = &forbidConfigReadsInTx{fakeSchedQualityStore: f.store}
	f.cycle()
	p := f.store.Policies[10].Policy
	p.TTFTSeconds = 2
	_, err := f.e.UpdatePolicy(context.Background(), 10, 0, p)
	require.NoError(t, err)
	b := q.DefaultRuleBParams()
	b.Confidence = .9
	_, err = f.e.UpdateTTFTRule(context.Background(), 0, b)
	require.NoError(t, err)
}
