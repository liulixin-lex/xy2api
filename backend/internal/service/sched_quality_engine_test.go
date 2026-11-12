//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type qualitySwitches struct{ enabled, tests bool }

func (s *qualitySwitches) SchedQualityEnabled(context.Context) bool     { return s.enabled }
func (s *qualitySwitches) SchedQualityTestEnabled(context.Context) bool { return s.tests }

type qualityFixture struct {
	t        *testing.T
	now      time.Time
	e        *SchedQualityEngineService
	store    *fakeSchedQualityStore
	ledger   *fakeSchedAttemptStore
	runner   *fakeSchedTestRunner
	switches *qualitySwitches
	seq      int
}

func newQualityFixture(t *testing.T) *qualityFixture {
	t.Helper()
	f := &qualityFixture{t: t, now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	f.store = &fakeSchedQualityStore{Now: func() time.Time { return f.now }, Members: map[int64][]int64{10: {1}}, Policies: map[int64]SchedPolicyRecord{10: {GroupID: 10, Policy: q.Policy{Enabled: true, TTFTSeconds: 10, MaxErrorRate: .05, MinCacheRatio: .8}}}}
	f.ledger = &fakeSchedAttemptStore{Now: func() time.Time { return f.now }}
	f.runner = &fakeSchedTestRunner{Records: map[SchedAccountModel][]SchedTestRecord{}}
	f.switches = &qualitySwitches{true, true}
	f.e = NewSchedQualityEngineService(f.store, f.ledger, f.runner, f.switches, func() time.Time { return f.now })
	return f
}
func (f *qualityFixture) cycle() { f.t.Helper(); require.NoError(f.t, f.e.cycle(context.Background())) }
func (f *qualityFixture) view(g int64) q.StateView {
	return f.e.Snapshot().View.StateOf(g, 1, "gpt-6.1-sol")
}
func (f *qualityFixture) samples(slow int) {
	for i := 0; i < 40; i++ {
		f.seq++
		ms := 1000
		if i < slow {
			ms = 30000
		}
		sample := q.AttemptSample{AttemptID: fmt.Sprint(f.seq), AccountID: 1, GroupID: 10, Model: "gpt-6.1-sol", StartedAt: f.now.Add(-time.Duration(40-i) * time.Millisecond), FinishedAt: f.now, Stream: true, Warm: true, Class: q.ClassRegular, Outcome: q.OutcomeOK, TTFOMs: &ms, SessionKey: fmt.Sprint(i % 4), RequestID: fmt.Sprint(f.seq)}
		f.e.Observe(sample)
	}
}
func (f *qualityFixture) cool() {
	f.samples(16)
	f.cycle()
	require.Equal(f.t, q.StateCooling, f.view(10).State)
	require.Equal(f.t, q.ReasonSpeed, f.view(10).Reason)
}
func (f *qualityFixture) due() { f.now = f.view(10).Until }
func qualityTest(at time.Time, slow int, cacheBad bool) SchedTestRecord {
	r := SchedTestRecord{AccountID: 1, Model: "gpt-6.1-sol", TestResult: q.TestResult{ID: at.String(), StartedAt: at, FinishedAt: at.Add(time.Second), Done: true}}
	n := 0
	for tier := q.TierSmall; tier <= q.TierLarge; tier++ {
		for turn := 1; turn <= 3; turn++ {
			ms, prompt, prev, cached := 1000, 10000, 8000, 8000
			if turn > 1 {
				if n < slow {
					ms = 30000
				}
				n++
			}
			if cacheBad {
				cached = 1000
			}
			r.Turns = append(r.Turns, q.TestTurn{Tier: tier, Turn: turn, Warm: turn > 1, Class: q.ClassRegular, Outcome: q.OutcomeOK, TTFOMs: &ms, PromptTokens: &prompt, PrevPromptTokens: &prev, CacheReadTokens: &cached})
		}
	}
	return r
}
func (f *qualityFixture) test(slow int, cacheBad bool) {
	pair := SchedAccountModel{1, "gpt-6.1-sol"}
	f.runner.Records[pair] = append(f.runner.Records[pair], qualityTest(f.now.Add(-time.Second), slow, cacheBad))
}

func TestSchedQualityCoolingRecoveryAndRoundIsolation(t *testing.T) {
	f := newQualityFixture(t)
	f.test(0, false)
	f.cool()
	require.Equal(t, 30*time.Second, f.view(10).Until.Sub(f.now))
	f.due()
	f.cycle()
	require.Len(t, f.runner.Requests, 1)
	require.True(t, f.view(10).WaitingTest)
	f.test(0, false)
	f.cycle()
	require.Equal(t, q.StateNormal, f.view(10).State)
	require.Empty(t, f.view(10).Reason)
	require.Equal(t, 1, f.view(10).Level)
	f.now = f.now.Add(5 * time.Second)
	f.cycle()
	require.Equal(t, q.StateNormal, f.view(10).State)
	require.Zero(t, f.e.Snapshot().GroupRows[1][0].Test.Tests)
	f.now = f.now.Add(q.LevelDecayAfter)
	f.cycle()
	require.Zero(t, f.view(10).Level)
	f.now = f.now.Add(q.WindowMaxAge)
	f.cycle()
	require.Empty(t, f.store.Rows)
	require.Equal(t, q.StateNormal, f.view(10).State)
}
func TestSchedQualityPooledTests(t *testing.T) {
	for _, secondSlow := range []int{0, 1} {
		t.Run(fmt.Sprint(secondSlow), func(t *testing.T) {
			f := newQualityFixture(t)
			f.cool()
			f.due()
			f.test(1, false)
			f.cycle()
			require.Equal(t, q.StateCooling, f.view(10).State)
			require.True(t, f.view(10).WaitingTest)
			require.Equal(t, 6, f.e.Snapshot().GroupRows[1][0].Test.SpeedWarm)
			f.now = f.now.Add(q.TestRetryEvery)
			f.cycle()
			require.Len(t, f.runner.Requests, 2)
			f.test(secondSlow, false)
			f.cycle()
			if secondSlow == 0 {
				require.Equal(t, q.StateNormal, f.view(10).State)
				require.Empty(t, f.view(10).Reason)
			} else {
				require.Equal(t, q.StateCooling, f.view(10).State)
				require.Equal(t, 2, f.view(10).Level)
				require.Equal(t, q.ReasonTestSpeed, f.view(10).Reason)
			}
			require.Zero(t, f.e.Snapshot().GroupRows[1][0].Test.Tests)
		})
	}
}
func TestSchedQualityCooldownLadder(t *testing.T) {
	f := newQualityFixture(t)
	f.cool()
	durations := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, d := range durations {
		require.Equal(t, d, f.view(10).Until.Sub(f.view(10).Since))
		require.Equal(t, min(i+1, 6), f.view(10).Level)
		if i == len(durations)-1 {
			break
		}
		f.due()
		f.test(2, false)
		f.cycle()
		require.Equal(t, q.ReasonTestSpeed, f.view(10).Reason)
	}
}
func TestSchedQualityRealDataAndRunningTests(t *testing.T) {
	for _, slow := range []int{0, 5, 16} {
		t.Run(fmt.Sprint(slow), func(t *testing.T) {
			f := newQualityFixture(t)
			f.cool()
			f.due()
			f.samples(slow)
			r := qualityTest(f.now.Add(-time.Second), 2, false)
			r.Running = true
			r.Done = false
			f.runner.Records[SchedAccountModel{1, r.Model}] = []SchedTestRecord{r}
			f.cycle()
			require.Empty(t, f.runner.Requests)
			if slow == 0 {
				require.Equal(t, q.StateNormal, f.view(10).State)
				require.Empty(t, f.view(10).Reason)
			} else {
				require.Equal(t, 2, f.view(10).Level)
				require.Equal(t, q.ReasonSpeed, f.view(10).Reason)
			}
			require.False(t, f.view(10).FromTest[0])
		})
	}
}
func TestSchedQualityTestsDisabledAndGateDisabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slow   int
		test   bool
		want   q.State
		reason q.Reason
	}{
		{"no conclusion", 0, false, q.StateNormal, q.ReasonUnverified},
		{"completed fail", 2, true, q.StateCooling, q.ReasonTestSpeed},
		{"completed pass", 0, true, q.StateNormal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQualityFixture(t)
			f.cool()
			f.due()
			f.switches.tests = false
			if tc.test {
				f.test(tc.slow, false)
			}
			f.cycle()
			require.Equal(t, tc.want, f.view(10).State)
			require.Equal(t, tc.reason, f.view(10).Reason)
			require.Empty(t, f.runner.Requests)
			if !tc.test {
				f.now = f.now.Add(time.Second)
				f.samples(16)
				f.cycle()
				require.Equal(t, 2, f.view(10).Level, "unverified recovery keeps the previous cooldown level")
			}
		})
	}
	t.Run("gate off and acute", func(t *testing.T) {
		f := newQualityFixture(t)
		p := f.store.Policies[10]
		p.Policy.Enabled = false
		f.store.Policies[10] = p
		f.samples(40)
		f.test(6, true)
		f.cycle()
		require.Equal(t, q.StateNormal, f.view(10).State)
		f.now = f.now.Add(time.Second)
		f.acute(3)
		f.cycle()
		require.Equal(t, q.ReasonAcuteTimeout, f.view(10).Reason)
		f.due()
		f.cycle()
		require.Equal(t, q.ReasonUnverified, f.view(10).Reason)
		require.Empty(t, f.runner.Requests)
	})
}
func TestSchedQualityOtherTestDimensionBlocksRecovery(t *testing.T) {
	f := newQualityFixture(t)
	f.cool()
	f.due()
	f.test(0, true)
	f.cycle()
	require.Equal(t, q.ReasonTestCache, f.view(10).Reason)
	require.Equal(t, 2, f.view(10).Level)
}

type qualityRetryRunner struct {
	*fakeSchedTestRunner
	now     func() time.Time
	started []time.Time
}

func (r *qualityRetryRunner) Request(ctx context.Context, a int64, m string, auto bool, op int64) (SchedTestTicket, error) {
	if n := len(r.started); n > 0 && r.now().Sub(r.started[n-1]) < q.TestRetryEvery {
		return SchedTestTicket{Why: "too_soon"}, nil
	}
	r.started = append(r.started, r.now())
	pair := SchedAccountModel{a, m}
	r.Records[pair] = append(r.Records[pair], SchedTestRecord{AccountID: a, Model: m, AbortReason: "not_schedulable", TestResult: q.TestResult{StartedAt: r.now(), FinishedAt: r.now()}})
	return r.fakeSchedTestRunner.Request(ctx, a, m, auto, op)
}
func TestSchedQualityUnverifiedRetryAndLogs(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	f := newQualityFixture(t)
	rr := &qualityRetryRunner{fakeSchedTestRunner: f.runner, now: func() time.Time { return f.now }}
	f.e.runner = rr
	f.cool()
	f.due()
	until := f.now
	for i := 0; i < 120; i++ {
		f.now = until.Add(time.Duration(i) * q.EvalInterval)
		f.cycle()
	}
	require.Len(t, rr.started, 10)
	f.now = until.Add(q.TestWaitMax)
	f.cycle()
	require.Equal(t, q.ReasonUnverified, f.view(10).Reason)
	require.Equal(t, 1, f.view(10).Level)
	require.Contains(t, logs.String(), `"via":"no_conclusion"`)
	require.Contains(t, logs.String(), `"component":"sched_quality"`)
	require.Contains(t, logs.String(), `"level":"INFO"`)
	require.NotContains(t, logs.String(), "!BADKEY")
	f.now = f.now.Add(q.LevelDecayAfter)
	f.cycle()
	require.Empty(t, f.view(10).Reason)
	require.Zero(t, f.view(10).Level)
}
func TestSchedQualityDecayBeforeFailure(t *testing.T) {
	for _, acute := range []bool{false, true} {
		t.Run(fmt.Sprint(acute), func(t *testing.T) {
			f := newQualityFixture(t)
			f.store.Rows = []q.StateRow{{AccountID: 1, Model: "gpt-6.1-sol", GroupID: 10, Level: 4, Since: f.now.Add(-q.LevelDecayAfter)}}
			if acute {
				f.acute(3)
			} else {
				f.samples(16)
			}
			f.cycle()
			require.Equal(t, 1, f.view(10).Level)
			require.Equal(t, 30*time.Second, f.view(10).Until.Sub(f.now))
		})
	}
}
func TestSchedQualityManualTestsAndRealPrecedence(t *testing.T) {
	for _, real := range []bool{false, true} {
		t.Run(fmt.Sprint(real), func(t *testing.T) {
			f := newQualityFixture(t)
			f.test(2, false)
			if real {
				f.samples(0)
			}
			f.cycle()
			if real {
				require.Equal(t, q.StateNormal, f.view(10).State)
			} else {
				require.Equal(t, q.ReasonTestSpeed, f.view(10).Reason)
			}
		})
	}
}
func TestSchedQualitySharedTestDifferentGroupsAndRounds(t *testing.T) {
	f := newQualityFixture(t)
	f.store.Members[20] = []int64{1}
	p := f.store.Policies[10]
	p.GroupID = 20
	p.Policy.TTFTSeconds = .5
	f.store.Policies[20] = p
	f.store.Rows = []q.StateRow{
		{AccountID: 1, Model: "gpt-6.1-sol", GroupID: 10, State: q.StateCooling, Level: 1, Reason: q.ReasonSpeed, Since: f.now.Add(-4 * time.Minute), Until: f.now.Add(-3 * time.Minute)},
		{AccountID: 1, Model: "gpt-6.1-sol", GroupID: 20, State: q.StateCooling, Level: 1, Reason: q.ReasonSpeed, Since: f.now.Add(-time.Minute), Until: f.now},
	}
	f.test(0, false)
	f.cycle()
	require.Equal(t, q.StateNormal, f.view(10).State)
	require.Equal(t, q.ReasonTestSpeed, f.view(20).Reason)
	f.now = f.view(20).Until
	f.cycle()
	require.True(t, f.view(20).WaitingTest)
	require.Zero(t, f.e.Snapshot().GroupRows[1][1].Test.Tests)
}
func TestSchedQualityOldTestsNeverRevive(t *testing.T) {
	f := newQualityFixture(t)
	start := f.now.Add(-3 * time.Minute)
	r := qualityTest(start, 2, false)
	r.FinishedAt = f.now
	f.runner.Records[SchedAccountModel{1, r.Model}] = []SchedTestRecord{r}
	f.store.Rows = []q.StateRow{{AccountID: 1, Model: r.Model, GroupID: 10, Since: start.Add(30 * time.Second)}}
	f.cycle()
	require.Equal(t, q.StateNormal, f.view(10).State)
	f.now = start.Add(q.TestFreshFor + time.Minute)
	f.cycle()
	require.Empty(t, f.store.Rows)
	require.Equal(t, q.StateNormal, f.view(10).State)
}
func (f *qualityFixture) acute(n int) {
	for i := 0; i < n; i++ {
		f.seq++
		f.e.Observe(q.AttemptSample{AttemptID: fmt.Sprint(f.seq), AccountID: 1, Model: "gpt-6.1-sol", StartedAt: f.now.Add(-time.Duration(n-i) * time.Millisecond), Stream: true, Outcome: q.OutcomeTimeout})
	}
}
func TestSchedQualityAcuteSurvivesSuccessFlood(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newQualityFixture(t)
			f.acute(3)
			count := 200
			if stream {
				count = 64
			}
			for i := 0; i < count; i++ {
				f.seq++
				ms := 1000
				f.e.Observe(q.AttemptSample{AttemptID: fmt.Sprint(f.seq), AccountID: 1, Model: "gpt-6.1-sol", StartedAt: f.now.Add(time.Duration(i) * time.Nanosecond), Stream: stream, Outcome: q.OutcomeOK, TTFOMs: &ms})
			}
			f.cycle()
			require.Equal(t, q.ReasonAcuteTimeout, f.view(10).Reason)
			f.due()
			f.switches.tests = false
			f.cycle()
			require.Equal(t, q.StateNormal, f.view(10).State)
			f.now = f.now.Add(time.Second)
			f.cycle()
			require.Equal(t, q.StateNormal, f.view(10).State)
		})
	}
}
func TestSchedQualityOverridesAndMembership(t *testing.T) {
	f := newQualityFixture(t)
	ctx := context.Background()
	require.NoError(t, f.e.SetOverride(ctx, SchedOverride{AccountID: 1, Mode: q.OverrideExempt}))
	expires := f.now.Add(10 * time.Second)
	require.NoError(t, f.e.SetOverride(ctx, SchedOverride{AccountID: 1, GroupID: 10, Mode: q.OverrideDemote, ExpiresAt: &expires}))
	old := f.e.Snapshot()
	v := old.View.StateOf(10, 1, "unknown")
	require.Equal(t, q.OverrideExempt, v.AllOverride.Mode)
	require.Equal(t, q.OverrideDemote, v.GroupOverride.Mode)
	f.samples(16)
	f.cycle()
	require.Equal(t, q.StateCooling, f.view(10).State)
	require.Equal(t, q.OverrideExempt, f.view(10).AllOverride.Mode)
	f.now = f.now.Add(15 * time.Second)
	f.cycle()
	require.Len(t, f.e.Snapshot().Overrides, 1)
	require.Equal(t, q.OverrideDemote, old.View.StateOf(10, 1, "unknown").GroupOverride.Mode)
	require.NoError(t, f.e.SetOverride(ctx, SchedOverride{AccountID: 1, GroupID: 10, Mode: q.OverrideExempt}))
	f.store.Members[10] = nil
	f.now = f.now.Add(30 * time.Second)
	f.cycle()
	require.Empty(t, f.store.Rows)
	require.Len(t, f.store.OverrideRows, 1)
	require.Len(t, f.e.Snapshot().Overrides, 1)
	require.Zero(t, f.e.Snapshot().Overrides[0].GroupID)
	require.NoError(t, f.e.ClearOverride(ctx, 1, 0))
	require.Empty(t, f.e.Snapshot().Overrides)
}
func TestSchedQualityResetRejectsOldEvidence(t *testing.T) {
	f := newQualityFixture(t)
	f.cool()
	f.test(2, false)
	f.acute(2)
	var called []time.Time
	f.e.OnAccountReset(func(id int64, at time.Time) { require.Equal(t, int64(1), id); called = append(called, at) })
	at, err := f.e.Reset(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, f.now, at)
	require.Empty(t, f.store.Rows)
	require.Len(t, called, 1)
	f.samples(40)
	f.e.Observe(q.AttemptSample{AttemptID: "late-old", AccountID: 1, Model: "gpt-6.1-sol", StartedAt: at.Add(-time.Second), Stream: true, Outcome: q.OutcomeTimeout})
	f.now = f.now.Add(time.Second)
	f.acute(1)
	f.cycle()
	require.Equal(t, q.StateNormal, f.view(10).State)
	require.Len(t, called, 1)
	require.Zero(t, f.e.Snapshot().Evidence[SchedAccountModel{1, "gpt-6.1-sol"}].Speed.N)
	f.store.Resets[1] = f.now
	f.cycle()
	require.Len(t, called, 2)
}
func TestSchedQualityStartupRecovery(t *testing.T) {
	f := newQualityFixture(t)
	f.samples(16)
	require.NoError(t, f.ledger.InsertBatch(context.Background(), f.e.window.take(f.now)[SchedAccountModel{1, "gpt-6.1-sol"}]))
	f.cycle()
	before := f.view(10)
	fresh := NewSchedQualityEngineService(f.store, f.ledger, f.runner, f.switches, func() time.Time { return f.now })
	fresh.Start(context.Background())
	fresh.Stop()
	after := fresh.Snapshot().View.StateOf(10, 1, "gpt-6.1-sol")
	require.Equal(t, before, after)
	require.Equal(t, 40, fresh.Snapshot().Evidence[SchedAccountModel{1, "gpt-6.1-sol"}].Samples)
}
func TestSchedQualityDelayedLedgerVisibility(t *testing.T) {
	f := newQualityFixture(t)
	f.cycle()
	ctx := context.Background()
	for _, delay := range []time.Duration{45 * time.Second, 4 * time.Second} {
		f.ledger.DelayVisible = true
		ms := 1000
		row := q.AttemptSample{AttemptID: delay.String(), AccountID: 1, Model: "delayed", StartedAt: f.now, Outcome: q.OutcomeOK, TTFOMs: &ms}
		if delay == 4*time.Second {
			require.NoError(t, f.ledger.InsertBatch(ctx, []q.AttemptSample{row}))
		}
		f.now = f.now.Add(delay)
		f.e.Observe(q.AttemptSample{})
		if delay == 45*time.Second {
			require.NoError(t, f.ledger.InsertBatch(ctx, []q.AttemptSample{row}))
		}
		// Another writer moves the cursor before the delayed statement commits.
		f.ledger.DelayVisible = false
		require.NoError(t, f.ledger.InsertBatch(ctx, []q.AttemptSample{{AttemptID: "advance-" + delay.String(), AccountID: 2, Model: "cursor", StartedAt: f.now, Outcome: q.OutcomeOK}}))
		f.cycle()
		f.ledger.Reveal()
		f.cycle()
	}
	require.Equal(t, 2, f.e.Snapshot().Evidence[SchedAccountModel{1, "delayed"}].Samples)
}

type qualityPageStore struct {
	*fakeSchedAttemptStore
	read func(time.Time, int) ([]q.AttemptSample, time.Time, error)
}

func (s *qualityPageStore) LoadInsertedAfter(_ context.Context, after time.Time, limit int) ([]q.AttemptSample, time.Time, error) {
	return s.read(after, limit)
}
func TestSchedQualityLedgerPaginationCursor(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(fmt.Sprint(startup), func(t *testing.T) {
			f := newQualityFixture(t)
			old := f.now.Add(-time.Minute)
			f.e.loaded = !startup
			f.e.insertedAt = old
			page := make([]q.AttemptSample, schedQualityLedgerPageSize)
			for i := range page {
				page[i] = q.AttemptSample{AttemptID: fmt.Sprint(i), AccountID: 1, Model: "pages", StartedAt: f.now, InsertedAt: f.now, Outcome: q.OutcomeOK}
			}
			calls := 0
			fail := true
			ps := &qualityPageStore{fakeSchedAttemptStore: f.ledger}
			ps.read = func(after time.Time, limit int) ([]q.AttemptSample, time.Time, error) {
				calls++
				require.Equal(t, schedQualityLedgerPageSize, limit)
				if calls%2 == 1 {
					return page, f.now, nil
				}
				require.Equal(t, f.now, after)
				if fail {
					return nil, time.Time{}, errors.New("page two")
				}
				return []q.AttemptSample{{AttemptID: "last", AccountID: 2, Model: "page-two", StartedAt: f.now, Outcome: q.OutcomeOK}}, f.now.Add(time.Second), nil
			}
			f.e.attempts = ps
			require.Error(t, f.e.loadEvidence(context.Background(), f.now))
			require.Equal(t, old, f.e.insertedAt)
			require.Equal(t, !startup, f.e.loaded)
			fail = false
			require.NoError(t, f.e.loadEvidence(context.Background(), f.now))
			require.Equal(t, 4, calls)
			require.Equal(t, f.now.Add(time.Second), f.e.insertedAt)
			require.Len(t, f.e.window.take(f.now)[SchedAccountModel{1, "pages"}], q.WindowMaxSamples)
		})
	}
}

type qualityHookStore struct {
	*fakeSchedQualityStore
	hook   func()
	missed chan struct{}
	after  func()
	err    error
}

func (s *qualityHookStore) RunCycle(ctx context.Context, fn func(SchedQualityTx) error) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	locked, err := s.fakeSchedQualityStore.RunCycle(ctx, func(tx SchedQualityTx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if s.hook != nil {
			s.hook()
		}
		return nil
	})
	if !locked && s.missed != nil {
		close(s.missed)
	}
	if locked && err == nil && s.after != nil {
		s.after()
	}
	return locked, err
}
func TestSchedQualityTwoEnginesSingleWriter(t *testing.T) {
	f := newQualityFixture(t)
	f.store.Members[20] = []int64{1}
	f.store.Policies[20] = f.store.Policies[10]
	f.cool()
	f.due()
	entered, release, missed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	hs := &qualityHookStore{fakeSchedQualityStore: f.store, hook: func() { close(entered); <-release }, missed: missed}
	f.e.store = hs
	second := NewSchedQualityEngineService(hs, f.ledger, f.runner, f.switches, func() time.Time { return f.now })
	require.NoError(t, second.refreshConfig(context.Background(), f.now))
	results := make(chan error, 2)
	go func() { results <- f.e.cycle(context.Background()) }()
	<-entered
	go func() { results <- second.cycle(context.Background()) }()
	<-missed
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.Len(t, f.runner.Requests, 1)
	require.Equal(t, f.view(10), second.Snapshot().View.StateOf(10, 1, "gpt-6.1-sol"))
	hs.hook = nil
	hs.missed = nil
	require.NoError(t, second.cycle(context.Background()))
	require.Equal(t, f.view(10), second.Snapshot().View.StateOf(10, 1, "gpt-6.1-sol"))
}
func TestSchedQualityResetWaitsForPublication(t *testing.T) {
	f := newQualityFixture(t)
	f.cool()
	committed, release := make(chan struct{}), make(chan struct{})
	hs := &qualityHookStore{fakeSchedQualityStore: f.store, after: func() { close(committed); <-release }}
	f.e.store = hs
	done := make(chan error, 2)
	go func() { done <- f.e.cycle(context.Background()) }()
	<-committed
	require.False(t, f.e.mu.TryLock())
	started := make(chan struct{})
	go func() { close(started); _, err := f.e.Reset(context.Background(), 1); done <- err }()
	<-started
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.Equal(t, q.StateNormal, f.view(10).State)
	require.Empty(t, f.store.Rows)
	require.Equal(t, f.now, f.e.Snapshot().ResetAt[1])
}
func TestSchedQualityFailureKeepsSnapshotAndGlobalSwitch(t *testing.T) {
	f := newQualityFixture(t)
	f.cool()
	old := f.e.Snapshot()
	f.now = f.now.Add(2 * time.Minute)
	hs := &qualityHookStore{fakeSchedQualityStore: f.store, err: errors.New("database unavailable")}
	f.e.store = hs
	require.Error(t, f.e.cycle(context.Background()))
	require.Same(t, old, f.e.Snapshot())
	hs.err = nil
	f.switches.enabled = false
	f.cycle()
	require.False(t, f.e.Snapshot().GlobalEnabled)
	require.Equal(t, q.StateCooling, f.view(10).State)
	require.Empty(t, f.runner.Requests)
	require.True(t, f.e.Snapshot().At.After(old.At))
}
func TestSchedQualitySnapshotOwnsDataAndTakeover(t *testing.T) {
	f := newQualityFixture(t)
	f.test(0, false)
	f.cycle()
	old := f.e.Snapshot()
	pair := SchedAccountModel{1, "gpt-6.1-sol"}
	*f.runner.Records[pair][0].Turns[0].TTFOMs = 999
	require.Equal(t, 1000, *old.Tests[pair].Turns[0].TTFOMs)
	require.Len(t, old.GroupRows[1], 1)
	require.Contains(t, old.Evidence, pair)
	require.Equal(t, q.StateView{}, old.View.StateOf(10, 1, "no-row"))
	for _, idle := range []int{-1, 0, 300, 900, 1800} {
		p := f.store.Policies[10]
		p.Policy.TakeoverIdleSeconds = idle
		f.store.Policies[10] = p
		f.now = f.now.Add(30 * time.Second)
		f.cycle()
		d, on := f.e.Snapshot().View.TakeoverIdle(10)
		require.Equal(t, idle >= 0, on)
		if on {
			require.Equal(t, time.Duration(idle)*time.Second, d)
		}
	}
}
func TestSchedQualityPolicyValidationAndCAS(t *testing.T) {
	f := newQualityFixture(t)
	ctx := context.Background()
	p := f.store.Policies[10].Policy
	f.store.Members[20] = nil
	record, err := f.e.Policy(ctx, 20)
	require.NoError(t, err)
	require.False(t, record.Policy.Enabled)
	_, err = f.e.UpdatePolicy(ctx, 10, 0, p)
	require.NoError(t, err)
	_, err = f.e.UpdatePolicy(ctx, 10, 0, p)
	require.ErrorIs(t, err, ErrSchedPolicyConflict)
	for _, idle := range []int{-2, 1, 301} {
		p.TakeoverIdleSeconds = idle
		require.Error(t, ValidateSchedQualityPolicy(p))
	}
}

func TestSchedQualitySingleWriterStateCommit(t *testing.T) {
	f := newQualityFixture(t)
	f.samples(16)
	pair := SchedAccountModel{1, "gpt-6.1-sol"}
	require.NoError(t, f.ledger.InsertBatch(context.Background(), f.e.window.take(f.now)[pair]))
	entered, release, missed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	hs := &qualityHookStore{fakeSchedQualityStore: f.store, hook: func() { close(entered); <-release }, missed: missed}
	f.e.store = hs
	second := NewSchedQualityEngineService(hs, f.ledger, f.runner, f.switches, func() time.Time { return f.now })
	require.NoError(t, second.refreshConfig(context.Background(), f.now))
	done := make(chan error, 2)
	go func() { done <- f.e.cycle(context.Background()) }()
	<-entered
	go func() { done <- second.cycle(context.Background()) }()
	<-missed
	require.NoError(t, <-done)
	require.Equal(t, q.StateNormal, second.Snapshot().View.StateOf(10, 1, pair.Model).State)
	close(release)
	require.NoError(t, <-done)
	require.Len(t, f.store.Rows, 1)
	require.Equal(t, 1, f.store.Rows[0].Level)
	hs.hook = nil
	hs.missed = nil
	require.NoError(t, second.cycle(context.Background()))
	require.Equal(t, f.view(10), second.Snapshot().View.StateOf(10, 1, pair.Model))
	require.Empty(t, f.runner.Requests)
}

func TestSchedQualityInitialResetTimesAreBaseline(t *testing.T) {
	f := newQualityFixture(t)
	ctx := context.Background()
	historical := map[int64]time.Time{1: f.now.Add(-72 * time.Hour), 2: f.now.Add(-24 * time.Hour)}
	f.store.Resets = cloneResetTimes(historical)
	var calls []int64
	f.e.OnAccountReset(func(id int64, at time.Time) { calls = append(calls, id) })
	failed := true
	f.e.attempts = &qualityPageStore{fakeSchedAttemptStore: f.ledger, read: func(time.Time, int) ([]q.AttemptSample, time.Time, error) {
		if failed {
			return nil, time.Time{}, errors.New("ledger unavailable at startup")
		}
		return nil, time.Time{}, nil
	}}
	require.Error(t, f.e.cycle(ctx))
	require.Empty(t, calls)
	f.now = f.now.Add(time.Minute)
	failed = false
	f.cycle()
	require.Empty(t, calls, "the first successful publication must not replay historical resets")
	require.Equal(t, historical, f.e.seenResets)
	f.store.Resets[1] = f.now
	f.cycle()
	require.Equal(t, []int64{1}, calls)
	f.cycle()
	require.Equal(t, []int64{1}, calls, "unchanged timestamps must not notify again")
}

func TestSchedQualityFailedCyclesTrimLocalWindow(t *testing.T) {
	f := newQualityFixture(t)
	pair := SchedAccountModel{1, "offline"}
	f.e.attempts = &qualityPageStore{fakeSchedAttemptStore: f.ledger, read: func(time.Time, int) ([]q.AttemptSample, time.Time, error) {
		require.Empty(t, f.e.window.pending, "local evidence must be drained before reading the ledger")
		return nil, time.Time{}, errors.New("ledger unavailable")
	}}
	for cycle := 0; cycle < 10; cycle++ {
		for i := 0; i < 40; i++ {
			f.e.Observe(q.AttemptSample{AttemptID: fmt.Sprintf("%d-%d", cycle, i), AccountID: 1, Model: pair.Model, StartedAt: f.now, Outcome: q.OutcomeOK})
		}
		require.Error(t, f.e.cycle(context.Background()))
		require.Len(t, f.e.window.samples[pair], min((cycle+1)*40, q.WindowMaxSamples))
		f.now = f.now.Add(q.EvalInterval)
	}
	f.now = f.now.Add(q.WindowMaxAge)
	require.Error(t, f.e.cycle(context.Background()))
	require.Empty(t, f.e.window.samples)
	require.Empty(t, f.e.window.pending)
	require.Zero(t, f.e.Snapshot().Version)
}

func TestSchedQualityMutationsPreserveEvaluationAge(t *testing.T) {
	f := newQualityFixture(t)
	f.cool()
	ctx := context.Background()
	evaluatedAt := f.e.Snapshot().At
	cold := f.view(10)
	f.e.attempts = &qualityPageStore{fakeSchedAttemptStore: f.ledger, read: func(time.Time, int) ([]q.AttemptSample, time.Time, error) {
		return nil, time.Time{}, errors.New("ledger unavailable")
	}}
	f.now = f.now.Add(q.ViewMaxAge + time.Second)
	require.Error(t, f.e.cycle(ctx))
	mutations := []struct {
		name  string
		apply func() error
	}{
		{"set override", func() error { return f.e.SetOverride(ctx, SchedOverride{AccountID: 2, Mode: q.OverrideDemote}) }},
		{"clear override", func() error { return f.e.ClearOverride(ctx, 2, 0) }},
		{"policy", func() error { _, err := f.e.UpdatePolicy(ctx, 10, 0, f.store.Policies[10].Policy); return err }},
		{"reset", func() error { _, err := f.e.Reset(ctx, 2); return err }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			version := f.e.Snapshot().Version
			require.NoError(t, mutation.apply())
			snapshot := f.e.Snapshot()
			require.Equal(t, version+1, snapshot.Version)
			require.Equal(t, evaluatedAt, snapshot.At)
			require.Equal(t, evaluatedAt, snapshot.View.At())
			require.Equal(t, cold.State, f.view(10).State)
			auto := snapshot.View.GlobalEnabled() && f.now.Sub(snapshot.View.At()) <= q.ViewMaxAge
			require.Equal(t, q.StandFront, q.StandingOf(f.view(10), auto, f.now), "a mutation cannot make stale automatic cooling effective again")
			f.now = f.now.Add(q.EvalInterval)
		})
	}
	f.e.attempts = f.ledger
	f.cycle()
	require.Equal(t, f.now, f.e.Snapshot().At)
	require.Equal(t, f.now, f.e.Snapshot().View.At())
}
