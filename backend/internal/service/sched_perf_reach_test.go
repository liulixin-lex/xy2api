//go:build unit

package service

import (
	"fmt"
	"math"
	"runtime"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedAdminPolicyReachUsesMemberEvidenceFromAllGroups(t *testing.T) {
	s, e, repo := schedAdminFixture()
	at := s.d.Now()
	facts := map[string]SchedPerfFact{}
	add := func(account int64, model string, turns int, bothGroups bool, read int64) {
		for i := range turns {
			group := int64(8) // Policy enabled in this source group only.
			if bothGroups && i >= turns/2 {
				group = 9 // No policy; same session label identifies a different session here.
			}
			f := SchedPerfFact{Account: account, Group: group, Model: model, Session: "session", First: true, Stream: true,
				Start: at.Add(-time.Hour).UnixMicro(), Finish: at.Add(-time.Minute).UnixMicro(),
				TTFO: 12400, Read: read, Input: 100}
			if i < 4 {
				f.TTFO = 12600
			} else if i == 4 {
				f.TTFO = 12500
			}
			facts[fmt.Sprint(len(facts))] = f
		}
	}
	add(1, "alpha", 20, true, 80)
	add(1, "beta", 20, true, 85)
	add(2, "alpha", 19, true, 100)
	add(3, "alpha", 20, false, 100)
	add(4, "alpha", 20, true, 100)
	add(5, "alpha", 20, true, 100) // Nonmember, even if returned by a stale ID list.
	add(5, "nonmember-model", 20, true, 100)
	for id := int64(2); id <= 5; id++ {
		repo.rows[id] = &Account{ID: id, GroupIDs: []int64{1}}
	}
	repo.rows[5].GroupIDs = []int64{8}
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{1: {ID: 1, Platform: PlatformOpenAI}}, []int64{1, 2, 3, 4, 5}}
	policy := q.DefaultPolicy()
	policy.Enabled, policy.TTFTSeconds, policy.MinCacheRatio = true, 12.5, .86
	e.policy = SchedPolicyRecord{GroupID: 1, Policy: policy}
	e.rule.Params = q.DefaultRuleBParams()
	e.perf = buildSchedPerf(facts, nil, map[int64]SchedPolicyRecord{8: {Policy: policy}},
		map[int64]time.Time{4: at.Add(-time.Hour)}, at)
	require.NotContains(t, e.perf.rows, schedQualityKey{1, "alpha", 9})
	require.NotContains(t, e.perf.reach, SchedAccountModel{4, "alpha"}, "at or before a reset does not qualify")

	preview := func() SchedAdminPolicyPreview {
		out, err := s.PreviewPolicy(t.Context(), 1, 0, policy)
		require.NoError(t, err)
		return out
	}
	initial := preview()
	require.Equal(t, []SchedAdminPolicyReach{
		{Model: "alpha", Cache: SchedAdminReachCount{N: 1}, FirstOutput: SchedAdminReachCount{OK: 1, N: 1}},
		{Model: "beta", Cache: SchedAdminReachCount{N: 1}, FirstOutput: SchedAdminReachCount{OK: 1, N: 1}},
	}, initial.Reach)
	policy.MinCacheRatio = .80
	after := preview()
	require.Equal(t, []SchedAdminPolicyReach{
		{Model: "alpha", Cache: SchedAdminReachCount{OK: 1, N: 1}, FirstOutput: SchedAdminReachCount{OK: 1, N: 1}},
		{Model: "beta", Cache: SchedAdminReachCount{OK: 1, N: 1}, FirstOutput: SchedAdminReachCount{OK: 1, N: 1}},
	}, after.Reach)
	require.Equal(t, []any{
		map[string]any{"model": "alpha", "cache": map[string]any{"ok": float64(1), "n": float64(1)}, "first_output": map[string]any{"ok": float64(1), "n": float64(1)}},
		map[string]any{"model": "beta", "cache": map[string]any{"ok": float64(1), "n": float64(1)}, "first_output": map[string]any{"ok": float64(1), "n": float64(1)}},
	}, schedAdminJSON(t, after)["reach"])

	e.rule.Params.Tolerance = .19
	require.Equal(t, SchedAdminReachCount{N: 1}, preview().Reach[0].FirstOutput)
	e.rule.Params.Tolerance = .20
	policy.TTFTSeconds = 12.4
	require.Equal(t, SchedAdminReachCount{N: 1}, preview().Reach[0].FirstOutput, "the exact 12.5-second output is slow at 12.4")
	policy.TTFTSeconds = 12.5
	e.snapshot.ResetAt = map[int64]time.Time{1: at.Add(time.Second)}
	reset := preview().Reach[0]
	require.Equal(t, SchedAdminReachCount{}, reset.Cache)
	require.Equal(t, SchedAdminReachCount{}, reset.FirstOutput)
}

func TestSchedPerfReachFirstOutputEligibility(t *testing.T) {
	facts := map[string]SchedPerfFact{}
	f := perfFact(1, 1)
	f.TTFO = 12400
	for i := range 20 {
		facts[fmt.Sprint(i)] = f
	}
	f.TTFO, f.Outcome, f.FullTier, f.Session = 0, q.OutcomeTimeout, true, "timeout"
	for i, wait := range []int{12400, 12500, 12600, -1} {
		f.Censored = wait
		facts[fmt.Sprintf("timeout-%d", i)] = f
	}
	f.Censored, f.FullTier = 13000, false
	facts["short-timeout"] = f
	f.TTFO, f.First = 13000, false
	facts["retry"] = f
	f.First, f.Stream = true, false
	facts["non-stream"] = f
	f.Stream = true
	f.TTFO, f.FullTier, f.Session = 12500, true, "session"
	facts["observed-clock-wins-over-timeout"] = f
	f.Start = perfAt.Add(-schedPerfWindow - 2*time.Second).UnixMicro()
	f.Finish = perfAt.Add(-schedPerfWindow - time.Second).UnixMicro()
	facts["window-before"] = f
	f.Start, f.Finish = perfAt.Add(-time.Second).UnixMicro(), perfAt.UnixMicro()
	facts["window-after"] = f
	row := buildSchedPerf(facts, nil, nil, nil, perfAt).reach[SchedAccountModel{1, "text"}]
	for _, x := range []struct {
		limit   uint64
		n, slow int
		enough  bool
	}{
		{12400, 24, 4, true},
		{12500, 23, 2, true},
		{12501, 22, 1, true},
		{12600, 22, 1, true},
		{12601, 21, 0, false}, // The second session is now entirely unknown.
	} {
		n, slow, enough := row.countFirst(x.limit)
		require.Equal(t, x.n, n, x.limit)
		require.Equal(t, x.slow, slow, x.limit)
		require.Equal(t, x.enough, enough, x.limit)
	}
}

func BenchmarkSchedPerfReachPublication(b *testing.B) {
	for _, count := range []int{28944, 70000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			facts := make(map[string]SchedPerfFact, count)
			for i := range count {
				f := perfFact(int64(i%300+1), int64(i%11+1))
				f.Model, f.Session = fmt.Sprintf("model-%02d", i/300%16), fmt.Sprint(i%11001)
				f.TTFO, f.Read, f.Input = 9000+(i*173)%12000, 80, 100
				if i%75 == 0 {
					f.TTFO, f.Censored, f.Outcome, f.FullTier = 0, 20000, q.OutcomeTimeout, true
				}
				facts[fmt.Sprint(i)] = f
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			p := buildSchedPerf(facts, nil, nil, nil, perfAt)
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(p)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric((float64(after.HeapAlloc)-float64(before.HeapAlloc))/(1024*1024), "MiB-live")
			for range b.N {
				buildSchedPerf(facts, nil, nil, nil, perfAt)
			}
		})
	}
}

func BenchmarkSchedPerfReachPreview(b *testing.B) {
	facts := make(map[string]SchedPerfFact, 70000)
	members := make(map[int64]string, 300)
	for i := range 70000 {
		f := perfFact(int64(i%300+1), int64(i%11+1))
		f.Model, f.Session = fmt.Sprintf("model-%02d", i/300%16), fmt.Sprint(i%11001)
		f.TTFO, f.Read, f.Input = 9000+(i*173)%12000, 80, 100
		facts[fmt.Sprint(i)] = f
		members[f.Account] = ""
	}
	p := buildSchedPerf(facts, nil, nil, nil, perfAt)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p.policyReach(members, nil, q.DefaultPolicy(), .2)
	}
}

func TestSchedPerfReachAdjacentRatios(t *testing.T) {
	policy := q.DefaultPolicy()
	policy.TTFTSeconds, policy.MinCacheRatio = 12.5, .8
	members := map[int64]string{1: ""}
	for _, slow := range []int{29, 30, 31} {
		t.Run(fmt.Sprint(slow), func(t *testing.T) {
			facts := map[string]SchedPerfFact{}
			for i := range 100 {
				f := perfFact(1, 9)
				f.Session, f.Read, f.Input, f.TTFO = fmt.Sprint(i%2), 80, 100, 12500
				if i < slow {
					f.TTFO = 12600
				}
				facts[fmt.Sprint(i)] = f
			}
			p := buildSchedPerf(facts, nil, nil, nil, perfAt)
			for _, tolerance := range []float64{math.Nextafter(.3, 0), .3, math.Nextafter(.3, 1)} {
				ok := 0
				if slow < 30 || slow == 30 && tolerance >= .3 {
					ok = 1
				}
				require.Equal(t, SchedAdminReachCount{OK: ok, N: 1}, p.policyReach(members, nil, policy, tolerance)[0].FirstOutput, "tolerance=%.17g", tolerance)
			}
			for _, minimum := range []float64{math.Nextafter(.8, 0), .8, math.Nextafter(.8, 1)} {
				candidate := policy
				candidate.MinCacheRatio = minimum
				ok := 1
				if minimum > .8 {
					ok = 0
				}
				require.Equal(t, SchedAdminReachCount{OK: ok, N: 1}, p.policyReach(members, nil, candidate, .3)[0].Cache, "minimum=%.17g", minimum)
			}
		})
	}
}

func TestSchedPerfReachWindowAndResetEndpoints(t *testing.T) {
	cutoff := perfAt.Add(-12 * time.Hour)
	facts := map[string]SchedPerfFact{}
	for model, finish := range map[string]time.Time{
		"before": cutoff.Add(-time.Microsecond), "at-cutoff": cutoff,
		"inside": perfAt.Add(-time.Microsecond), "at-end": perfAt,
	} {
		f := perfFact(1, 9)
		f.Model, f.Start, f.Finish = model, cutoff.Add(-time.Hour).UnixMicro(), finish.UnixMicro()
		facts[model] = f
	}
	p := buildSchedPerf(facts, nil, nil, nil, perfAt)
	require.Len(t, p.reach, 2)
	require.Contains(t, p.reach, SchedAccountModel{1, "at-cutoff"})
	require.Contains(t, p.reach, SchedAccountModel{1, "inside"})
	require.Equal(t, []SchedAdminPolicyReach{{Model: "at-cutoff"}, {Model: "inside"}}, p.policyReach(map[int64]string{1: ""}, nil, q.DefaultPolicy(), .2))
	reset := perfAt.Add(-time.Minute)
	facts = map[string]SchedPerfFact{}
	for model, delta := range map[string]time.Duration{"before": -time.Microsecond, "equal": 0, "after": time.Microsecond} {
		f := perfFact(1, 9)
		f.Model, f.Start = model, reset.Add(delta).UnixMicro()
		facts[model] = f
	}
	p = buildSchedPerf(facts, nil, nil, map[int64]time.Time{1: reset}, perfAt)
	require.Len(t, p.reach, 1)
	require.Contains(t, p.reach, SchedAccountModel{1, "after"})
}

func TestSchedPerfReachPublishedSnapshotStaysImmutable(t *testing.T) {
	facts := map[string]SchedPerfFact{}
	for i := range 20 {
		f := perfFact(1, 9)
		f.Session, f.Read, f.Input, f.TTFO = fmt.Sprint(i%2), 80, 100, 12500
		if i < 4 {
			f.TTFO = 12600
		}
		facts[fmt.Sprint(i)] = f
	}
	policy := q.DefaultPolicy()
	policy.TTFTSeconds, policy.MinCacheRatio = 12.5, .8
	members := map[int64]string{1: ""}
	engine := newQualityFixture(t).e
	first := buildSchedPerf(facts, nil, nil, nil, perfAt)
	engine.performance.Store(first)
	held := engine.PerformanceSnapshot()
	before := held.policyReach(members, nil, policy, .2)
	require.Equal(t, []SchedAdminPolicyReach{{Model: "text", Cache: SchedAdminReachCount{OK: 1, N: 1}, FirstOutput: SchedAdminReachCount{OK: 1, N: 1}}}, before)
	// Reuse and overwrite the worker's source map, then publish its next version.
	for id, f := range facts {
		f.TTFO, f.Read = 99999, 0
		facts[id] = f
	}
	engine.performance.Store(buildSchedPerf(facts, nil, nil, nil, perfAt.Add(schedPerfTick)))
	require.NotSame(t, held, engine.PerformanceSnapshot())
	require.Equal(t, []SchedAdminPolicyReach{{Model: "text", Cache: SchedAdminReachCount{N: 1}, FirstOutput: SchedAdminReachCount{N: 1}}}, engine.PerformanceSnapshot().policyReach(members, nil, policy, .2))
	require.Equal(t, before, held.policyReach(members, nil, policy, .2))
	// Also pin both adjacent representable tolerances at the default 4/20 ratio.
	for _, tolerance := range []float64{math.Nextafter(.2, 0), .2, math.Nextafter(.2, 1)} {
		ok := 1
		if tolerance < .2 {
			ok = 0
		}
		require.Equal(t, SchedAdminReachCount{OK: ok, N: 1}, held.policyReach(members, nil, policy, tolerance)[0].FirstOutput)
	}
}
