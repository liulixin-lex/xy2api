//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type qualitySwitches struct{ enabled bool }

func (s *qualitySwitches) SchedQualityEnabled(context.Context) bool { return s.enabled }

type qualityFixture struct {
	t        *testing.T
	now      time.Time
	e        *SchedQualityEngineService
	store    *fakeSchedQualityStore
	ledger   *fakeSchedAttemptStore
	switches *qualitySwitches
}

func newQualityFixture(t *testing.T) *qualityFixture {
	t.Helper()
	f := &qualityFixture{t: t, now: time.Date(2026, 1, 1, 12, 0, 10, 0, time.UTC)}
	f.store = &fakeSchedQualityStore{Now: func() time.Time { return f.now }, Members: map[int64][]int64{10: {1}}, Policies: map[int64]SchedPolicyRecord{10: {GroupID: 10, Policy: q.Policy{Enabled: true, TTFTSeconds: 1, MaxErrorRate: .05, MinCacheRatio: .8, TakeoverIdleSeconds: 900}}}}
	f.ledger = &fakeSchedAttemptStore{Now: func() time.Time { return f.now }}
	f.switches = &qualitySwitches{true}
	f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
	return f
}
func engineSamples(n int, at time.Time, stream bool) []q.AttemptSample {
	rows := make([]q.AttemptSample, n)
	for i := range rows {
		ms := 2000
		rows[i] = q.AttemptSample{AttemptID: fmt.Sprintf("synthetic-%s-%d", at.Format(time.RFC3339Nano), i), RequestID: fmt.Sprintf("request-%d", i), AccountID: 1, GroupID: 10, Model: "synthetic-model", StartedAt: at.Add(-3 * time.Second), FinishedAt: at, Outcome: q.OutcomeOK, Stream: stream, TTFOMs: &ms}
	}
	return rows
}
func (f *qualityFixture) cycle() {
	f.t.Helper()
	require.NoError(f.t, f.e.cycle(context.Background()))
}
func (f *qualityFixture) view() q.StateView {
	return f.e.Snapshot().View.StateOf(10, 1, "synthetic-model")
}

func TestValidatePolicyErrorFloor(t *testing.T) {
	for _, tc := range []struct {
		name            string
		ttfo, errorRate float64
		valid           bool
	}{
		{"floor", .001, .05, true}, {"unbounded-first-output", 1000, .05, true}, {"too-low-error", 1, .049, false}, {"zero-first-output", 0, .05, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSchedQualityPolicy(q.Policy{TTFTSeconds: tc.ttfo, MaxErrorRate: tc.errorRate, MinCacheRatio: .8, TakeoverIdleSeconds: 900})
			require.Equal(t, tc.valid, err == nil)
		})
	}
}

func (f *qualityFixture) badTTFT() {
	f.store.Members[10] = []int64{1, 2}
	rows := engineSamples(80, f.now.Add(-30*time.Second), true)
	for i := range rows {
		rows[i].SessionKey = fmt.Sprintf("s-%d", i%3)
	}
	f.ledger.Rows = append(f.ledger.Rows, rows...)
	for i, r := range rows {
		r.AccountID = 2
		r.AttemptID = fmt.Sprintf("peer-%d", i)
		r.RequestID = r.AttemptID
		ms := 100
		r.TTFOMs = &ms
		f.ledger.Rows = append(f.ledger.Rows, r)
	}
}
func (f *qualityFixture) line(line q.Dimension) q.LineState {
	for _, r := range f.store.Rows {
		if r.AccountID == 1 && r.GroupID == 10 && r.Line == line {
			return r
		}
	}
	return q.LineState{}
}
func TestEngineThreeLinesAndAdjacentEvaluations(t *testing.T) {
	f := newQualityFixture(t)
	rows := engineSamples(50, f.now.Add(-30*time.Second), false)
	for i := 0; i < 6; i++ {
		rows[i].Outcome = q.OutcomeAccountFault
		rows[i].Class = q.ClassUpstream
	}
	f.ledger.Rows = rows
	f.cycle()
	require.Equal(t, q.IdentityUnknown, f.view().Error.Identity)
	require.Equal(t, f.now.Add(-10*time.Second), f.line(q.DimensionError).PendingAt)
	require.Empty(t, f.store.Decisions)
	f.now = f.now.Add(5 * time.Minute)
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().Error.Identity)
	require.Equal(t, q.IdentityUnknown, f.view().TTFO.Identity)
	require.Len(t, f.store.Decisions, 1)
	require.Equal(t, q.ActionEnter, f.store.Decisions[0].Action)
	require.Equal(t, 20*time.Minute, f.line(q.DimensionError).Until.Sub(f.line(q.DimensionError).Entry))
}
func TestEngineRestartIdempotentAndExactReview(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	original := append([]q.LineState(nil), f.store.Rows...)
	decisions := len(f.store.Decisions)
	restarted := NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
	require.NoError(t, restarted.cycle(context.Background()))
	require.Equal(t, original, f.store.Rows)
	require.Len(t, f.store.Decisions, decisions)
	until := f.view().TTFO.Until
	f.now = until.Add(9 * time.Second)
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	f.now = until.Add(10 * time.Second)
	f.cycle()
	require.True(t, f.view().TTFO.Trial)
	require.Equal(t, until, f.line(q.DimensionTTFO).Epoch)
	require.Equal(t, q.ActionTrial, f.store.Decisions[len(f.store.Decisions)-1].Action)
}
func TestEngineTrialAvailabilityUsesLaterInsertion(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	at := f.now.Add(-10 * time.Second)
	f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Trial: true, Epoch: at.Add(-time.Minute), TrialUntil: at, Admissions: 1}}
	for i := range f.ledger.Rows {
		f.ledger.Rows[i].InsertedAt = at
	}
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	require.Equal(t, q.ActionTrialFail, f.store.Decisions[0].Action)
	require.Equal(t, at, f.line(q.DimensionTTFO).Entry)
}
func TestEngineResetExemptionAndDisabledGroup(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.store.OverrideRows = []SchedOverride{{AccountID: 1, GroupID: 10, Mode: q.OverrideExempt}}
	f.switches.enabled = false
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	require.Equal(t, q.PlaceFront, f.view().Placement(true, f.now))
	at, err := f.e.Reset(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, at, f.e.Snapshot().View.ResetAt(1))
	require.Equal(t, q.IdentityUnknown, f.view().TTFO.Identity)
	require.Len(t, f.store.OverrideRows, 1)
	f.now = f.now.Add(5 * time.Minute)
	f.cycle()
	require.Equal(t, q.IdentityUnknown, f.view().TTFO.Identity)
	p := f.store.Policies[10].Policy
	p.Enabled = false
	_, err = f.e.UpdatePolicy(context.Background(), 10, 0, p)
	require.NoError(t, err)
	require.Empty(t, f.store.Rows)
}
func TestEngineReadOnlyFollowerAndMemberRemoval(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.cycle()
	before := append([]q.LineState(nil), f.store.Rows...)
	f.store.lock.Lock()
	f.now = f.now.Add(time.Minute)
	f.cycle()
	f.store.lock.Unlock()
	require.Equal(t, before, f.store.Rows)
	require.True(t, f.e.processedAt.IsZero())
	f.store.Members[10] = []int64{2}
	f.e.configAt = time.Time{}
	f.cycle()
	for _, r := range f.store.Rows {
		require.NotEqual(t, int64(1), r.AccountID)
	}
}
func TestEngineRuleChangesAtomicRejudgeAndConflict(t *testing.T) {
	for _, change := range []string{"tighten", "relax", "mixed"} {
		t.Run(change, func(t *testing.T) {
			f := newQualityFixture(t)
			f.badTTFT()
			f.cycle()
			old := f.line(q.DimensionTTFO)
			old.Level = 3
			old.Admissions = 4
			old.Epoch = f.now.Add(-time.Minute)
			for i, r := range f.store.Rows {
				if r.LineKey == old.LineKey {
					f.store.Rows[i] = old
				}
			}
			// A rear peer whose epoch excludes its samples must be cleared before
			// membership and evidence are frozen for the relaxed reevaluation.
			for i, r := range f.store.Rows {
				if r.AccountID == 2 && r.Line == q.DimensionTTFO {
					r.Identity = q.IdentityRear
					r.Epoch = f.now
					r.Until = f.now.Add(time.Hour)
					f.store.Rows[i] = r
				}
			}
			before := append([]q.LineState(nil), f.store.Rows...)
			peers := make(map[q.LineKey]q.LineState)
			for _, r := range before {
				peers[r.LineKey] = r
			}
			frozen := freezePeers(f.now, peers)
			f.e.peerBuckets = map[time.Time]*peerBucket{frozen.at: frozen}
			decisionCount := len(f.store.Decisions)
			p := q.DefaultRuleBParams()
			switch change {
			case "tighten":
				p.Confidence = .7
			case "relax":
				p.Confidence = .9
			case "mixed":
				p.Confidence = .9
				p.Tolerance = .19
			}
			result, err := f.e.UpdateTTFTRule(context.Background(), 0, p)
			require.NoError(t, err)
			require.Equal(t, int64(1), result.Version)
			if change == "tighten" {
				require.Equal(t, before, f.store.Rows)
				require.Zero(t, result.Cleared)
			} else {
				require.Equal(t, 2, result.Cleared)
				r := f.line(q.DimensionTTFO)
				require.Equal(t, q.IdentityRear, r.Identity)
				require.Zero(t, r.Level)
				require.Equal(t, 1, r.Admissions)
				require.Equal(t, f.now, r.Epoch)
				require.Equal(t, f.now, r.Entry)
				require.NotNil(t, r.Basis.TTFT.PRel, "the cleared rear peer must supply usable evidence")
				var actions []q.Action
				for _, d := range f.store.Decisions[decisionCount:] {
					if d.Key == r.LineKey {
						actions = append(actions, d.Action)
						if d.Action == q.ActionRelax {
							require.True(t, d.Until.IsZero())
						} else {
							require.Equal(t, r.Until, d.Until)
						}
					}
				}
				require.Equal(t, []q.Action{q.ActionRelax, q.ActionEnter}, actions)
				require.Equal(t, q.IdentityPass, f.e.Snapshot().View.StateOf(10, 2, "synthetic-model").TTFO.Identity)
			}
			rows := append([]q.LineState(nil), f.store.Rows...)
			_, err = f.e.UpdateTTFTRule(context.Background(), 0, p)
			require.ErrorIs(t, err, ErrSchedPolicyConflict)
			require.Equal(t, rows, f.store.Rows)
		})
	}
}
func TestEnginePolicyRelaxesOnlySelectedGroupAndLine(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.store.Members[20] = []int64{1, 2}
	f.store.Policies[20] = SchedPolicyRecord{GroupID: 20, Policy: f.store.Policies[10].Policy}
	f.cycle()
	before := append([]q.LineState(nil), f.store.Rows...)
	p := f.store.Policies[10].Policy
	p.TTFTSeconds = 10
	result, err := f.e.UpdatePolicy(context.Background(), 10, 0, p)
	require.NoError(t, err)
	require.Equal(t, []q.Dimension{q.DimensionTTFO}, result.RelaxedLines)
	require.Equal(t, 2, result.Cleared)
	for _, old := range before {
		if old.GroupID == 10 && old.Line == q.DimensionTTFO {
			continue
		}
		found := false
		for _, r := range f.store.Rows {
			if r.LineKey == old.LineKey {
				require.Equal(t, old, r)
				found = true
			}
		}
		require.True(t, found)
	}
	require.Equal(t, q.IdentityPass, f.view().TTFO.Identity)
}
func TestEngineRuleCommitFailureKeepsConfigAndSnapshot(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.cycle()
	before := f.e.Snapshot()
	rows := append([]q.LineState(nil), f.store.Rows...)
	f.store.CommitErr = fmt.Errorf("synthetic commit failure")
	p := q.DefaultRuleBParams()
	p.Tolerance = .3
	_, err := f.e.UpdateTTFTRule(context.Background(), 0, p)
	require.ErrorIs(t, err, f.store.CommitErr)
	require.Same(t, before, f.e.Snapshot())
	require.Equal(t, rows, f.store.Rows)
	require.Zero(t, f.store.Rule.Version)
	require.Equal(t, q.DefaultRuleBParams(), f.e.rule.Params)
}

func TestEngineReassessmentPublishesActionsOnlyAfterCommit(t *testing.T) {
	output := captureSchedObsLogs(t)
	for _, commit := range []bool{false, true} {
		obs := NewSchedObservabilityService()
		f := newQualityFixture(t)
		f.e.obs = obs
		f.badTTFT()
		f.cycle()
		output.Reset()
		before := obs.window
		start := len(f.store.Decisions)
		if !commit {
			f.store.CommitErr = fmt.Errorf("synthetic commit failure")
		}

		p := q.DefaultRuleBParams()
		p.Confidence = .9
		_, err := f.e.UpdateTTFTRule(context.Background(), 0, p)
		actions := make(map[string]int)
		for _, record := range schedObsLogRecords(t, output) {
			if record["msg"] == "sched_quality.state" {
				action, ok := record["action"].(string)
				require.True(t, ok, "state log must carry a string action")
				actions[action]++
			}
		}
		if !commit {
			require.ErrorIs(t, err, f.store.CommitErr)
			require.Empty(t, actions)
			require.Len(t, f.store.Decisions, start)
			require.Equal(t, before, obs.window)
			continue
		}
		require.NoError(t, err)
		persisted := make(map[string]int)
		for _, decision := range f.store.Decisions[start:] {
			persisted[string(decision.Action)]++
		}
		require.Equal(t, 1, persisted[string(q.ActionRelax)])
		require.Equal(t, persisted, actions)
		require.EqualValues(t, len(f.store.Decisions)-start, (obs.window.cooled-before.cooled)+(obs.window.released-before.released)+(obs.window.trial-before.trial)+(obs.window.relax-before.relax)+(obs.window.cleared-before.cleared))
		require.EqualValues(t, 1, obs.window.relax-before.relax)
	}
}

func TestEngineSessionSlowTwice(t *testing.T) {
	f := newQualityFixture(t)
	rows := engineSamples(3, f.now.Add(-time.Minute), true)
	for i := range rows {
		rows[i].SessionKey = "session"
		rows[i].StartedAt = rows[i].StartedAt.Add(time.Duration(i) * time.Second)
	}
	rows[0].AccountID = 2
	f.ledger.Rows = rows
	f.cycle()
	require.False(t, f.e.Snapshot().View.SessionSlowTwice(10, 1, "synthetic-model", "session"), "the switched first turn leaves only one eligible round")
	require.True(t, f.e.window.samples[SchedAccountModel{1, "synthetic-model"}][0].Switched)
	require.False(t, f.e.Snapshot().View.SessionSlowTwice(10, 2, "synthetic-model", "session"))
	require.False(t, f.e.Snapshot().View.SessionSlowTwice(10, 1, "synthetic-model", "missing"))
}
