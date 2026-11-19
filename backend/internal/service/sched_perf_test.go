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
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
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
	for _, minute := range []int{10, 15, 30, 60} {
		for j := range 2 {
			f := perfFact(1, 1)
			f.Start = perfAt.Add(-time.Duration(minute)*time.Minute + time.Duration(j)*time.Microsecond).UnixMicro()
			facts[strconv.Itoa(len(facts))] = f
		}
	}
	p := buildSchedPerf(facts, nil, perfPolicies(), nil, perfAt)
	require.Equal(t, SchedPerfRecent{1, 5, 7, 0}, p.Recent(1, 1, "text"))
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
	require.Equal(t, 7, p.Recent(1, 1, "text").Slow60)
	delete(policies, 1)
	p = buildSchedPerf(facts, nil, policies, nil, perfAt)
	require.Empty(t, p.rows)
}

type perfReadStub struct {
	SchedAttemptStore
	page func(time.Time, time.Time, SchedAttemptCursor, bool) ([]SchedPerfFact, SchedAttemptCursor, bool, error)
}

func (p perfReadStub) LoadPerfPage(_ context.Context, from, at time.Time, after SchedAttemptCursor, inc bool, _ int) ([]SchedPerfFact, SchedAttemptCursor, bool, error) {
	return p.page(from, at, after, inc)
}
func (perfReadStub) LoadPerfConfig(context.Context) (map[int64]SchedPolicyRecord, map[int64]time.Time, error) {
	return perfPolicies(), nil, nil
}
func (perfReadStub) LoadPerfSwitches(context.Context, time.Time, time.Time, int64, int) ([]SchedPerfSwitch, error) {
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
	p := buildSchedPerf(map[string]SchedPerfFact{"1": f, "2": f2}, events, perfPolicies(), nil, perfAt)
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
