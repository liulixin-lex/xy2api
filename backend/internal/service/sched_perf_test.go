//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var perfAt = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

func perfPolicies() map[int64]SchedPolicyRecord {
	policy := SchedPolicyRecord{Policy: q.Policy{Enabled: true, TTFTSeconds: 2}}
	return map[int64]SchedPolicyRecord{1: policy, 2: policy}
}
func perfFact(a, g int64) SchedPerfFact {
	return SchedPerfFact{Account: a, Group: g, Model: "text", First: true, Stream: true, TTFO: 1000, Session: "session", Start: perfAt.Add(-time.Minute).UnixMicro(), Finish: perfAt.Add(-time.Second).UnixMicro(), Outcome: q.OutcomeOK}
}

func TestSchedPerfPooledScore(t *testing.T) {
	facts := make(map[string]SchedPerfFact)
	seq := 0
	add := func(a, g int64, warm bool, clock, n int) {
		for range n {
			f := perfFact(a, g)
			f.Warm, f.TTFO = warm, clock
			facts[strconv.Itoa(seq)] = f
			seq++
		}
	}
	// A: 8 warm, 2 cold per group. Peers: B=6 slow warm; C=4 fast cold.
	// p=.6, warm=(6+6)/16=.75, cold=(0+6)/14=3/7.
	for _, g := range []int64{1, 2} {
		add(1, g, true, 3000, 8)
		add(1, g, false, 1000, 2)
		add(2, g, true, 3000, 6)
		add(3, g, false, 1000, 4)
	}
	p := buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
	for _, g := range []int64{1, 2} {
		r := p.rows[schedQualityKey{1, "text", g}].First
		require.Equal(t, 10, r.N)
		require.Equal(t, 8, r.Slow)
		require.Equal(t, 1, r.Sessions)
		require.Equal(t, 10, r.ExpectedN)
		require.InDelta(t, 6.+6./7., *r.ExpectedSlow, 1e-12)
	}
	score := p.scores[SchedAccountModel{1, "text"}]
	require.Equal(t, 20, score.N)
	require.Equal(t, 2, score.Sessions)
	require.InDelta(t, 16./7., score.Excess, 1e-12)
	micro, ok := p.ScoreMicros(1, "text")
	require.True(t, ok)
	require.EqualValues(t, 57143, micro)
	require.True(t, p.Usable(perfAt.Add(10*time.Minute)))
	require.False(t, p.Usable(perfAt.Add(10*time.Minute+1)))
	delete(facts, "0")
	p = buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
	require.Equal(t, 19, p.scores[SchedAccountModel{1, "text"}].N)
	_, ok = p.ScoreMicros(1, "text")
	require.False(t, ok)
	delete(facts, "10") // One group now has nine peers.
	p = buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
	require.Nil(t, p.rows[schedQualityKey{1, "text", 1}].First.ExpectedSlow)
	for id, f := range facts {
		f.Group = 1
		facts[id] = f
	}
	add(1, 1, true, 3000, 1)
	p = buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
	score = p.scores[SchedAccountModel{1, "text"}]
	require.Equal(t, 20, score.N)
	require.Equal(t, 1, score.Sessions)
	require.Nil(t, score.Value)
}

func TestSchedPerfClocksAndRecent(t *testing.T) {
	facts := map[string]SchedPerfFact{}
	for _, minute := range []int{10, 15, 30} {
		for j := range 2 {
			f := perfFact(1, 1)
			f.Start = perfAt.Add(-time.Duration(minute)*time.Minute + time.Duration(j)*time.Microsecond).UnixMicro()
			facts[strconv.Itoa(len(facts))] = f
		}
	}
	p := buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
	require.Equal(t, SchedPerfRecent{First10: 1, First30: 5}, p.Recent(1, 1, "text"))
	require.Equal(t, 3, p.First15(1))
	for _, tc := range []struct {
		clock, lower int
		outcome      q.Outcome
		full         bool
		slow, known  int
	}{
		{2000, 0, q.OutcomeOK, false, 0, 1}, {2001, 0, q.OutcomeOK, false, 1, 1}, {0, 2000, q.OutcomeTimeout, true, 1, 1},
		{0, 1999, q.OutcomeTimeout, true, 0, 0}, {0, 3000, q.OutcomeTimeout, false, 0, 0}, {0, 3000, q.OutcomeCanceled, true, 0, 0}, {0, 0, q.OutcomeOK, false, 0, 0},
	} {
		f := perfFact(1, 1)
		f.TTFO, f.Censored, f.Outcome, f.FullTier = tc.clock, tc.lower, tc.outcome, tc.full
		p = buildSchedPerf(map[string]SchedPerfFact{"one": f}, nil, perfPolicies(), nil, perfAt)
		r := p.rows[schedQualityKey{1, "text", 1}].First
		require.Equal(t, tc.slow, r.Slow)
		require.Equal(t, tc.known, r.N)
		require.Equal(t, 1-tc.known, r.Unknown)
		require.Equal(t, 1, p.First15(1))
	}
	p = buildSchedPerf(facts, nil, perfPolicies(), map[int64]time.Time{1: perfAt.Add(-10 * time.Minute)}, perfAt)
	require.Equal(t, 1, p.First15(1))
	policies := perfPolicies()
	policies[1] = SchedPolicyRecord{Policy: q.Policy{Enabled: true, TTFTSeconds: .5}}
	p = buildSchedPerf(facts, nil, policies, nil, perfAt)
	require.Equal(t, 6, p.rows[schedQualityKey{1, "text", 1}].First.Slow)
	delete(policies, 1)
	p = buildSchedPerf(facts, nil, policies, nil, perfAt)
	require.Empty(t, p.rows)
}

type perfReadStub struct {
	dbNow time.Time
	SchedAttemptStore
	switches func(time.Time, time.Time) []SchedPerfSwitch
	page     func(time.Time, time.Time, SchedAttemptCursor, bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error)
}

func (p perfReadStub) PerfDatabaseTime(context.Context) (time.Time, error) {
	if p.dbNow.IsZero() {
		return perfAt, nil
	}
	return p.dbNow, nil
}

func (p perfReadStub) LoadPerfPage(_ context.Context, from, at time.Time, after SchedAttemptCursor, inc bool, _ int) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
	return p.page(from, at, after, inc)
}
func (perfReadStub) LoadPerfConfig(context.Context) (map[int64]SchedPolicyRecord, map[int64]time.Time, error) {
	return perfPolicies(), nil, nil
}
func (p perfReadStub) LoadPerfSwitches(_ context.Context, from, at time.Time, _ int64, _ int) ([]SchedPerfSwitch, error) {
	if p.switches != nil {
		return p.switches(from, at), nil
	}
	return nil, nil
}

func TestSchedPerfResumeAndAtomicRead(t *testing.T) {
	w := schedPerfStore{facts: map[string]SchedPerfFact{}, switches: map[int64]SchedPerfSwitch{}}
	f := perfFact(1, 1)
	f.ID = "first"
	next := SchedAttemptCursor{At: perfAt.Add(-time.Second), AttemptID: f.ID}
	fail := true
	ledger := perfReadStub{page: func(from, at time.Time, after SchedAttemptCursor, inc bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
		if inc {
			require.Equal(t, perfAt.Add(-10*time.Second), from)
			return []SchedPerfFact{f}, next, true, nil
		}
		if after.At.IsZero() {
			return []SchedPerfFact{f}, next, false, nil
		}
		require.Equal(t, next, after)
		if fail {
			return nil, SchedAttemptCursor{}, false, errors.New("interrupted")
		}
		return nil, next, true, nil
	}}
	engine := NewSchedQualityEngineService(nil, ledger, nil, func() time.Time { return perfAt }, nil)
	require.Error(t, engine.publishPerformance(t.Context(), &w, perfAt))
	require.False(t, engine.PerformanceSnapshot().complete)
	_, ok := engine.PerformanceSnapshot().ScoreMicros(1, "text")
	require.False(t, ok)
	fail = false
	require.NoError(t, engine.publishPerformance(t.Context(), &w, perfAt))
	snap := engine.PerformanceSnapshot()
	require.Equal(t, buildSchedPerf(map[string]SchedPerfFact{f.ID: f}, nil, perfPolicies(), nil, perfAt), snap)
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			for range 300 {
				p := engine.PerformanceSnapshot()
				if p.Recent(1, 1, "text").First10 != 1 {
					t.Error("partial publication")
				}
				p.ScoreMicros(1, "text")
			}
		})
	}
	for range 50 {
		require.NoError(t, engine.publishPerformance(t.Context(), &w, perfAt))
	}
	group.Wait()
}

type perfStartupLedger struct {
	*fakeSchedAttemptStore
	shortRecovery chan struct{}
}

func (l perfStartupLedger) LoadFinishedPage(ctx context.Context, from, at time.Time, c SchedAttemptCursor, n int) ([]q.AttemptSample, error) {
	<-l.shortRecovery
	return l.fakeSchedAttemptStore.LoadFinishedPage(ctx, from, at, c, n)
}
func TestSchedPerfStartsAfterShortRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newQualityFixture(t)
		ledger := perfStartupLedger{f.ledger, make(chan struct{})}
		entered := make(chan struct{})
		release := make(chan struct{})
		ledger.SchedPerfLedger = perfReadStub{page: func(_, _ time.Time, c SchedAttemptCursor, incremental bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
			if !incremental {
				close(entered)
				<-release
			}
			return nil, c, true, nil
		}}
		f.e.attempts = ledger
		started := make(chan struct{})
		go func() { f.e.Start(t.Context()); close(started) }()
		synctest.Wait()
		select {
		case <-entered:
			t.Error("long-window load started before short-window recovery")
		default:
		}
		close(ledger.shortRecovery)
		<-started
		<-entered
		require.Positive(t, f.e.Snapshot().Version)
		require.False(t, f.e.PerformanceSnapshot().Usable(f.now))
		close(release)
		f.e.Stop()
	})
}

func TestSchedPerfDatabaseWatermark(t *testing.T) {
	// A collector finishes before T; a lagging database commits it after T.
	// Both timestamps predate the ordinary ten-second overlap on the app clock.
	next := perfAt.Add(schedPerfTick)
	f := perfFact(1, 1)
	f.ID = "late"
	f.Finish = perfAt.Add(-30 * time.Second).UnixMicro()
	inserted := perfAt.Add(-45 * time.Second)
	ledger := perfReadStub{dbNow: perfAt.Add(-time.Minute)}
	ledger.page = func(from, at time.Time, c SchedAttemptCursor, inc bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
		if at.Equal(next) && inc && !from.After(inserted) {
			return []SchedPerfFact{f}, c, true, nil
		}
		return nil, c, true, nil
	}
	engine := NewSchedQualityEngineService(nil, ledger, nil, func() time.Time { return perfAt }, nil)
	w := schedPerfStore{facts: map[string]SchedPerfFact{}}
	require.NoError(t, engine.publishPerformance(t.Context(), &w, perfAt))
	require.NoError(t, engine.publishPerformance(t.Context(), &w, next))
	require.Equal(t, 1, engine.PerformanceSnapshot().First15(1))
}

func TestSchedPerfSwitchReconciliation(t *testing.T) {
	event := SchedPerfSwitch{ID: 1, Group: 1, Account: 1, Target: 2, Kind: stickyFollow, Request: "first", At: perfAt.Add(-30 * time.Second)}
	ledger := perfReadStub{
		page: func(_, _ time.Time, c SchedAttemptCursor, _ bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
			return nil, c, true, nil
		},
		switches: func(from, at time.Time) []SchedPerfSwitch {
			if from.After(event.At) {
				return nil
			}
			first := event
			if at.Equal(perfAt) {
				return []SchedPerfSwitch{first}
			}
			first.Model = "text"
			duplicate := first
			duplicate.ID = 2
			late := event
			late.ID = 3
			late.Request = "late"
			late.Kind = "takeover"
			return []SchedPerfSwitch{first, duplicate, late}
		},
	}
	engine := NewSchedQualityEngineService(nil, ledger, nil, func() time.Time { return perfAt }, nil)
	w := schedPerfStore{facts: map[string]SchedPerfFact{}, switches: map[int64]SchedPerfSwitch{}}
	require.NoError(t, engine.publishPerformance(t.Context(), &w, perfAt))
	unknown := schedQualityKey{account: 1, group: 1}
	require.Equal(t, 1, engine.PerformanceSnapshot().rows[unknown].Switches.Handoff)
	next := perfAt.Add(5 * time.Minute)
	require.NoError(t, engine.publishPerformance(t.Context(), &w, next))
	warm := engine.PerformanceSnapshot()
	coldStore := schedPerfStore{facts: map[string]SchedPerfFact{}, switches: map[int64]SchedPerfSwitch{}}
	require.NoError(t, engine.publishPerformance(t.Context(), &coldStore, next))
	require.Equal(t, engine.PerformanceSnapshot(), warm)
	require.Equal(t, schedPerfSwitches{Takeover: 1}, warm.rows[unknown].Switches)
	require.Equal(t, schedPerfSwitches{Handoff: 1}, warm.rows[schedQualityKey{account: 1, group: 1, model: "text"}].Switches)
}

func TestSchedPerfHalfMicroScores(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		ownSlow, ownN, peerSlow, peerN int
		want                           int64
	}{
		{"positive", 6, 20, 11, 64, 64063}, {"negative", 0, 44, 7, 40, -120313},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := map[string]SchedPerfFact{}
			add := func(account int64, slow, n int) {
				for i := range n {
					f := perfFact(account, 1)
					f.Session = strconv.Itoa(i % 2)
					if i < slow {
						f.TTFO = 3000
					}
					facts[strconv.Itoa(len(facts))] = f
				}
			}
			add(1, tc.ownSlow, tc.ownN)
			add(2, tc.peerSlow, tc.peerN)
			snap := buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
			micros, ok := snap.ScoreMicros(1, "text")
			require.True(t, ok)
			require.Equal(t, tc.want, micros)
			require.Equal(t, float64(tc.want)/1e6, *snap.scores[SchedAccountModel{1, "text"}].Value)
		})
	}
}

func TestSchedPerfMilliseconds(t *testing.T) {
	cases := []struct {
		sec          float64
		clock, lower int
		slow, known  int
	}{
		{1.001, 1001, 0, 0, 1}, {1.001, 1002, 0, 1, 1}, {2.007, 0, 2007, 1, 1}, {2.007, 0, 2006, 0, 0},
		{1.0016, 1002, 0, 0, 1}, {2.0074, 0, 2007, 1, 1}, {1.0015, 1002, 0, 0, 1},
		{2.0035, 2004, 0, 0, 1}, {2.0035, 2005, 0, 1, 1},
		{0.0004, 0, 0, 1, 1}, {0.0004, 0, -1, 0, 0},
		{1e300, 2004, 0, 0, 1}, {1e300, 0, 2007, 0, 0},
	}
	facts := make(map[string]SchedPerfFact)
	policies := make(map[int64]SchedPolicyRecord)
	for i, tc := range cases {
		group := int64(i + 1)
		f := perfFact(1, group)
		f.TTFO, f.Censored = tc.clock, tc.lower
		f.FullTier, f.Outcome = true, q.OutcomeTimeout
		facts[strconv.Itoa(i)] = f
		policies[group] = SchedPolicyRecord{Policy: q.Policy{Enabled: true, TTFTSeconds: tc.sec}}
	}
	snapshot := buildSchedPerf(facts, nil, policies, nil, perfAt)
	for i, tc := range cases {
		first := snapshot.rows[schedQualityKey{account: 1, group: int64(i + 1), model: "text"}].First
		require.Equal(t, tc.known, first.N, "known: %+v", tc)
		require.Equal(t, tc.slow, first.Slow, "slow: %+v", tc)
		require.Equal(t, 1-tc.known, first.Unknown, "unknown: %+v", tc)
	}
}

func TestSchedPerfAdminAndSwitches(t *testing.T) {
	admin, engine, accounts := schedAdminFixture()
	admin.d.Groups.(schedAdminGroups)[2] = &Group{ID: 2, Name: "previous", Platform: PlatformOpenAI}
	accounts.rows[1].Priority = 4
	f, f2 := perfFact(1, 1), perfFact(1, 2)
	events := map[int64]SchedPerfSwitch{}
	for i, kind := range []string{stickyFollow, stickyFollow, "create", "owner_released", "takeover", "migrate", "other"} {
		id := int64(i + 1)
		events[id] = SchedPerfSwitch{ID: id, At: perfAt.Add(-time.Minute), Account: 1, Group: 2, Target: 2, Model: "text", Kind: kind, Reason: "profit_floor", Request: "r"}
	}
	full, short, cancel := f2, f2, f2
	full.Outcome, full.FullTier = q.OutcomeTimeout, true
	short.Outcome, cancel.Outcome = q.OutcomeTimeout, q.OutcomeCanceled
	// The repository resolves the two successes of a single request before aggregation.
	firstSuccess, lastSuccess := f2, f2
	lastSuccess.First, lastSuccess.Delivered = false, true
	contA, contB := f2, f2
	contA.Session, contA.Read, contA.Input, contA.Delivered = "A", 20, 80, true
	contB.Session, contB.Read, contB.Input, contB.Delivered = "B", 30, 50, true
	left := f2
	left.Finish = perfAt.Add(-schedPerfWindow).UnixMicro()
	left.Start = left.Finish - 1
	left.Delivered = true
	expired := left
	expired.Start--
	expired.Finish--
	facts := map[string]SchedPerfFact{"other-group": f, "full": full, "short": short, "cancel": cancel,
		"first-success": firstSuccess, "last-success": lastSuccess, "cont-a": contA, "cont-b": contB, "left": left, "expired": expired}
	reset := perfAt.Add(-5 * time.Minute)
	for i, ts := range []time.Time{reset, reset.Add(-time.Microsecond)} {
		id := int64(100 + i)
		events[id] = SchedPerfSwitch{ID: id, At: ts, Account: 3, Group: 2, Target: 2, Model: "text", Kind: "takeover"}
	}
	events[102] = SchedPerfSwitch{ID: 102, At: perfAt.Add(-time.Minute), Account: 1, Group: 3, Target: 2, Model: "text", Kind: "migrate"}
	p := buildSchedPerf(facts, events, perfPolicies(), map[int64]time.Time{3: reset}, perfAt)
	engine.perf = p
	res, err := admin.AccountPerformance(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	require.Len(t, res.Groups, 2)
	require.True(t, res.Groups[1].GateEnabled)
	require.Equal(t, 2., res.Groups[1].TTFTSeconds)
	require.True(t, res.Rows[0].Member)
	require.False(t, res.Rows[1].Member)
	require.Equal(t, 4, res.Rows[1].Priority)
	assert.Equal(t, schedPerfAttempts{N: 8, Timeouts: 1, Failures: 1}, res.Rows[1].Attempts)
	assert.Equal(t, 4, res.Rows[1].Requests)
	assert.Equal(t, schedPerfCont{Turns: 2, Sessions: 2, ReadTokens: 50, InputTokens: 130}, res.Rows[1].Cont)

	require.Equal(t, schedPerfSwitches{1, 1, 1, 1, 1}, res.Rows[1].Switches)
	group, err := admin.GroupPerformance(t.Context(), 2)
	require.NoError(t, err)
	require.Equal(t, res.Rows[1:], group.Rows)
	raw, err := json.Marshal(group)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"value":null`)
	require.Contains(t, string(raw), `"expected_slow":null`)
	require.Contains(t, string(raw), `"last_eval":null`)
}

func TestSchedPerfAccountGroupScopeWithoutRows(t *testing.T) {
	admin, engine, accounts := schedAdminFixture()
	paused := SchedPolicyRecord{Policy: q.Policy{Enabled: false, TTFTSeconds: 6, MinCacheRatio: .4}}
	enabled := SchedPolicyRecord{Policy: q.Policy{Enabled: true, TTFTSeconds: 12, MinCacheRatio: .8}}
	published := map[int64]SchedPolicyRecord{1: paused, 2: enabled, 3: enabled}
	group2 := &Group{ID: 2, Name: "paused after publication", Platform: PlatformOpenAI}
	account := accounts.rows[1]
	account.Groups = append(account.Groups, group2)
	account.GroupIDs = append(account.GroupIDs, group2.ID)
	fact := perfFact(1, 1)
	fact.Delivered = true
	engine.perf = buildSchedPerf(map[string]SchedPerfFact{"delivered": fact}, nil, published, nil, perfAt)

	// Flip both gates, join an existing group and a newly created group before the next publication.
	engine.snapshot.At = perfAt.Add(30 * time.Second)
	admin.d.Now = func() time.Time { return engine.snapshot.At }
	engine.snapshot.Policies = map[int64]SchedPolicyRecord{1: enabled, 2: paused, 3: enabled, 4: enabled}
	for _, g := range []*Group{
		{ID: 3, Name: "new membership", Platform: PlatformOpenAI},
		{ID: 4, Name: "new group", Platform: PlatformOpenAI},
		{ID: 5, Name: "other platform", Platform: PlatformAnthropic},
	} {
		account.Groups = append(account.Groups, g)
		account.GroupIDs = append(account.GroupIDs, g.ID)
	}

	result, err := admin.AccountPerformance(t.Context(), account.ID)
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Empty(t, result.Rows)
	fallback := q.DefaultPolicy()
	require.Equal(t, []schedPerfGroup{
		{GroupID: 1, Name: "group", GateEnabled: false, TTFTSeconds: 6, MinCacheRatio: .4},
		{GroupID: 2, Name: group2.Name, GateEnabled: true, TTFTSeconds: 12, MinCacheRatio: .8},
		{GroupID: 3, Name: "new membership", GateEnabled: true, TTFTSeconds: 12, MinCacheRatio: .8},
		{GroupID: 4, Name: "new group", GateEnabled: false, TTFTSeconds: fallback.TTFTSeconds, MinCacheRatio: fallback.MinCacheRatio},
	}, result.Groups)
}

func TestSchedPerfAdminLineAssessments(t *testing.T) {
	admin, engine, _ := schedAdminFixture()
	now := engine.snapshot.At
	first := now.Add(-q.RegularInterval)
	state := q.LineState{}
	state.LastAssessment = q.RecordAssessment(state, first, q.EventRegular, q.VerdictPass, "", q.Basis{})
	state.EvaluatedAt = first
	firstEval := SchedAdminLastEvalFromState(state)
	state.LastAssessment = q.RecordAssessment(state, now, q.EventRegular, q.VerdictFail, q.ActionEnter, q.Basis{})
	state.EvaluatedAt = now
	require.Len(t, state.LastAssessment, 2)
	engine.snapshot.GroupRows[1] = []SchedGroupRow{{
		GroupID: 1, Model: "text",
		View:          q.StateView{TTFO: q.LineView{Identity: q.IdentityRear}, Cache: q.LineView{Trial: true, TrialUntil: now.Add(time.Hour)}},
		TTFTLastEval:  SchedAdminLastEvalFromState(state),
		CacheLastEval: firstEval,
		TTFTSamples:   q.NewSampleCounts(0, 8),
		ErrorSamples:  q.NewSampleCounts(2, 3),
		CacheSamples:  q.NewSampleCounts(0, 0),
	}, {
		GroupID: 1, Model: "text-retired", ErrorLastEval: firstEval,
	}}
	current := perfFact(1, 1)
	historical := current
	historical.Model = "text-retired"
	unmatched := current
	unmatched.Model = "text-unmatched"
	engine.perf = buildSchedPerf(map[string]SchedPerfFact{"current": current, "retired": historical, "unmatched": unmatched}, nil, perfPolicies(), nil, perfAt)

	detail, err := admin.Detail(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, detail.Groups, 1)
	require.Len(t, detail.Groups[0].ByModel, 2)
	want := detail.Groups[0].ByModel[0].Lines
	group, err := admin.GroupPerformance(t.Context(), 1)
	require.NoError(t, err)
	account, err := admin.AccountPerformance(t.Context(), 1)
	require.NoError(t, err)
	require.Equal(t, group.Rows, account.Rows)
	require.Len(t, account.Rows, 3)
	require.Equal(t, "text", account.Rows[0].Model)
	got := account.Rows[0].Lines
	require.NotNil(t, got.TTFO.LastEval)
	require.Equal(t, schedAdminStamp(now), got.TTFO.LastEval.At)
	require.Equal(t, q.ActionEnter, got.TTFO.LastEval.Action)
	require.Equal(t, want.TTFO.Identity, got.TTFO.Identity)
	require.Equal(t, want.Cache.TrialUntil, got.Cache.Until)
	require.Equal(t, want.TTFO.SampleCounts, got.TTFO.SampleCounts)
	require.Equal(t, want.Error.SampleCounts, got.Error.SampleCounts)
	require.Equal(t, want.Cache.SampleCounts, got.Cache.SampleCounts)
	require.Nil(t, got.Error.LastEval) // Legacy state still has current sample counts.
	require.NotNil(t, got.Cache.LastEval)
	require.Equal(t, schedAdminStamp(first), got.Cache.LastEval.At)

	require.Equal(t, "text-retired", account.Rows[1].Model)
	require.Equal(t, firstEval, account.Rows[1].Lines.Error.LastEval)
	require.Equal(t, "text-unmatched", account.Rows[2].Model)
	unmatchedLines := account.Rows[2].Lines
	for _, line := range []schedPerfLine{unmatchedLines.TTFO, unmatchedLines.Error, unmatchedLines.Cache} {
		raw, err := json.Marshal(line)
		require.NoError(t, err)
		require.JSONEq(t, `{"identity":"unknown","until":null,"last_eval":null,"samples_local":null,"samples_total":null}`, string(raw))
	}
}

func BenchmarkSchedPerfPublish50K(b *testing.B) {
	var base, loaded runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	facts := make(map[string]SchedPerfFact, 50000)
	for i := range 50000 {
		f := perfFact(int64(i%101+1), int64(i%2+1))
		f.Warm = i%3 == 0
		f.Model = strings.Clone("example-text")
		f.Session = fmt.Sprintf("%064d", i%5000)
		f.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		facts[f.ID] = f
	}
	policies := perfPolicies()
	snapshot := buildSchedPerf(facts, nil, policies, nil, perfAt)
	runtime.GC()
	runtime.ReadMemStats(&loaded)
	runtime.KeepAlive(snapshot)
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric((float64(loaded.HeapAlloc)-float64(base.HeapAlloc))/(1024*1024), "MiB-live")
	for range b.N {
		buildSchedPerf(facts, nil, policies, nil, perfAt)
	}
}
