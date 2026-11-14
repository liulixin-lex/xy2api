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

func TestEngineInstantOrder(t *testing.T) {
	f := newQualityFixture(t)
	minute := f.now.Add(-10 * time.Second)
	rows := engineSamples(50, minute, false)
	for i := 0; i < 6; i++ {
		rows[i].Outcome = q.OutcomeAccountFault
	}
	for i := 6; i < 12; i++ {
		rows[i].Stream = true
	}
	f.ledger.Rows = rows
	f.cycle()
	v := f.view()
	require.True(t, v.Failing)
	require.Equal(t, minute, v.Since)
	require.Equal(t, []q.Dimension{q.DimensionError}, v.Dims, "ordinary entry precedes same-instant fast entry")
	require.Len(t, f.store.Decisions, 1)
	require.Equal(t, "enter", f.store.Decisions[0].Action)
	require.Equal(t, 20*time.Minute, v.Until.Sub(v.Since))
}

func TestEngineCompletionOnlyFastAndMinuteOnlyRegular(t *testing.T) {
	f := newQualityFixture(t)
	completion := f.now.Add(-20 * time.Second) // 11:59:50, processed with the 12:00 ordinary minute
	f.ledger.Rows = engineSamples(6, completion, true)
	f.cycle()
	require.Equal(t, completion, f.view().Since)
	require.Equal(t, []q.Dimension{q.DimensionTTFO}, f.view().Dims)
	// Ordinary threshold reached between minute ticks cannot enter until next tick.
	g := newQualityFixture(t)
	g.now = f.now.Add(-15 * time.Second) // horizon 11:59:45
	at := g.now.Add(-11 * time.Second)
	g.ledger.Rows = engineSamples(50, at, false)
	for i := range g.ledger.Rows {
		g.ledger.Rows[i].Outcome = q.OutcomeAccountFault
	}
	g.cycle()
	require.False(t, g.view().Failing)
	g.now = f.now
	g.cycle()
	require.True(t, g.view().Failing)
	require.Equal(t, f.now.Add(-10*time.Second), g.view().Since)
}

func TestEngineReplayIdempotentAndExactDeadline(t *testing.T) {
	f := newQualityFixture(t)
	completion := f.now.Add(-20 * time.Second)
	f.ledger.Rows = engineSamples(6, completion, true)
	f.cycle()
	until := f.view().Until
	f.e.processedAt = time.Time{} // new leader/restart rereads persisted state
	f.cycle()
	require.Len(t, f.store.Decisions, 1)
	require.Equal(t, 1, f.view().Level)
	f.now = until.Add(9 * time.Second)
	f.cycle()
	require.True(t, f.view().Failing, "watermark has not reached the deadline")
	f.now = until.Add(10 * time.Second)
	f.cycle()
	require.False(t, f.view().Failing)
	require.Equal(t, until, f.view().EvidenceSince)
	require.Len(t, f.store.Decisions, 2)
	require.Equal(t, "release", f.store.Decisions[1].Action)
	f.e.processedAt = time.Time{}
	f.cycle()
	require.Len(t, f.store.Decisions, 2, "replay must not reuse pre-release attempts")
}

func TestEngineResetAndExemptionDoNotFreezeInternalState(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = engineSamples(6, f.now.Add(-20*time.Second), true)
	f.store.OverrideRows = []SchedOverride{{AccountID: 1, GroupID: 10, Mode: q.OverrideExempt}}
	f.switches.enabled = false
	f.cycle()
	require.True(t, f.view().Failing)
	require.False(t, f.e.Snapshot().View.GlobalEnabled())
	require.Equal(t, q.StandFront, q.StandingOf(f.view(), true, f.now), "exemption covers placement")
	reset, err := f.e.Reset(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, f.view().Failing)
	require.Equal(t, reset, f.e.Snapshot().View.ResetAt(1))
	require.Zero(t, f.e.Snapshot().Account(1).Speed.N)
	f.now = f.now.Add(time.Minute)
	f.cycle()
	require.False(t, f.view().Failing, "pre-reset dispatches cannot enter again")
}

func TestEngineGroupDisableAndReadOnlyFollower(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = engineSamples(6, f.now.Add(-20*time.Second), true)
	f.cycle()
	f.store.lock.Lock()
	f.e.processedAt = time.Time{}
	f.cycle()
	require.True(t, f.view().Failing)
	require.Len(t, f.store.Decisions, 1)
	require.True(t, f.e.processedAt.IsZero())
	f.store.lock.Unlock()
	p := f.store.Policies[10]
	p.Policy.Enabled = false
	f.store.Policies[10] = p
	f.e.configAt = time.Time{}
	f.cycle()
	require.Empty(t, f.store.Rows)
	require.False(t, f.e.Snapshot().View.GroupEnabled(10))
}

func TestEngineAccountMetricsAndModelCounts(t *testing.T) {
	f := newQualityFixture(t)
	rows := engineSamples(2, f.now.Add(-time.Minute), true)
	first, timeout := 100, 3000
	rows[0].TTFOMs = &first
	rows[1].Outcome, rows[1].TTFOMs, rows[1].CensoredMs = q.OutcomeTimeout, nil, &timeout
	rows[1].Model = "second-model"
	f.ledger.Rows = rows
	f.cycle()
	a := f.e.Snapshot().Account(1)
	require.Equal(t, 2, a.Speed.N)
	require.Equal(t, 100, *a.Speed.P50Ms, "nearest rank median")
	require.Equal(t, 2, a.Error.N)
	require.Equal(t, 1, a.Error.Bad)
	require.Len(t, f.e.Snapshot().GroupModels[10], 2)
	idle, on := f.e.Snapshot().View.TakeoverIdle(999)
	require.True(t, on)
	require.Equal(t, 15*time.Minute, idle)
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

func TestEnginePreviewUsesLastMinuteAndNoFastTrack(t *testing.T) {
	f := newQualityFixture(t)
	f.ledger.Rows = engineSamples(6, f.now.Add(-time.Second), true)
	f.cycle()
	changes, err := f.e.PreviewPolicy(context.Background(), 10, f.store.Policies[10].Policy)
	require.NoError(t, err)
	require.Empty(t, changes)
	require.Empty(t, f.store.Decisions)
	f = newQualityFixture(t)
	rows := engineSamples(20, f.now.Add(-time.Minute), true)
	for i := range rows {
		value := 500
		rows[i].TTFOMs = &value
	}
	require.NoError(t, f.ledger.InsertBatch(context.Background(), rows))
	f.now = f.now.Add(time.Minute)
	f.cycle()
	require.False(t, f.view().Failing)
	p := f.store.Policies[10].Policy
	p.TTFTSeconds = .1
	changes, err = f.e.PreviewPolicy(context.Background(), 10, p)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, []q.Dimension{q.DimensionTTFO}, changes[0].Dims)
	require.False(t, f.view().Failing, "preview must not mutate internal state")
}
