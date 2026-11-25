//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type reachReviewRuleStore struct {
	SchedQualityStore
	engine *SchedQualityEngineService
	held   bool
	calls  int
}

func (r *reachReviewRuleStore) LoadTTFTRule(context.Context) (SchedTTFTRuleRecord, error) {
	r.calls++
	if r.engine.mu.TryLock() {
		r.engine.mu.Unlock()
	} else {
		r.held = true
	}
	return SchedTTFTRuleRecord{Params: q.DefaultRuleBParams()}, nil
}

type reachReviewEngine struct {
	*schedAdminEngine
	real *SchedQualityEngineService
}

func (e *reachReviewEngine) TTFTRule(ctx context.Context) (SchedTTFTRuleRecord, error) {
	return e.real.TTFTRule(ctx)
}
func reachReviewFixture(t *testing.T) (*SchedAdminService, *reachReviewRuleStore, q.Policy) {
	t.Helper()
	s, e, _ := schedAdminFixture()
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{1: {ID: 1, Platform: PlatformOpenAI}}, []int64{1}}
	policy := q.DefaultPolicy()
	policy.TTFTSeconds = 12.5
	policy.MinCacheRatio = .8
	e.policy = SchedPolicyRecord{GroupID: 1, Version: 7, Policy: policy}
	e.rule.Params = q.DefaultRuleBParams()
	e.preview = func(p q.Policy) ([]SchedPolicyChange, error) {
		v := q.VerdictFail
		if p.TTFTSeconds > policy.TTFTSeconds {
			v = q.VerdictPass
		}
		return []SchedPolicyChange{{AccountID: 1, Model: "text", Lines: map[q.Dimension]q.Verdict{q.DimensionTTFO: v, q.DimensionCache: q.VerdictPass, q.DimensionError: q.VerdictUnknown}}}, nil
	}
	facts := map[string]SchedPerfFact{}
	for i := 0; i < 20; i++ {
		f := perfFact(1, int64(8+i%2))
		f.Start = s.d.Now().Add(-time.Minute).UnixMicro()
		f.Finish = s.d.Now().Add(-time.Second).UnixMicro()
		f.Read = 80
		f.Input = 100
		f.TTFO = 12500
		facts[fmt.Sprint(i)] = f
	}
	e.perf = buildSchedPerf(facts, nil, nil, nil, s.d.Now())
	r := &reachReviewRuleStore{}
	real := &SchedQualityEngineService{store: r}
	r.engine = real
	s.d.Engine = &reachReviewEngine{e, real}
	return s, r, policy
}
func TestReachReviewLegacyResponse(t *testing.T) {
	s, _, p := reachReviewFixture(t)
	p.TTFTSeconds = 25
	out, err := s.PreviewPolicy(t.Context(), 1, 7, p)
	require.NoError(t, err)
	raw := schedAdminJSON(t, out)
	delete(raw, "reach")
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	fmt.Printf("LEGACY_RESPONSE %s\n", b)
	require.JSONEq(t, `{"changed":[{"account_id":1,"from":"fail","line":"ttfo","model":"text","name":"one","to":"pass"}],"lines":{"cache":{"after":{"fail":0,"pass":1,"unknown":0},"now":{"fail":0,"pass":1,"unknown":0}},"error":{"after":{"fail":0,"pass":0,"unknown":1},"now":{"fail":0,"pass":0,"unknown":1}},"ttfo":{"after":{"fail":0,"pass":1,"unknown":0},"now":{"fail":1,"pass":0,"unknown":0}}},"relaxed_lines":["ttfo"]}`, string(b))
	require.Equal(t, []any{"ttfo"}, raw["relaxed_lines"])
	require.Equal(t, []any{map[string]any{"account_id": float64(1), "name": "one", "model": "text", "line": "ttfo", "from": "fail", "to": "pass"}}, raw["changed"])
	_, err = s.PreviewPolicy(t.Context(), 1, 6, p)
	require.ErrorIs(t, err, ErrSchedPolicyConflict)
}
func TestReachReviewNoAdditionalEngineLock(t *testing.T) {
	s, r, p := reachReviewFixture(t)
	_, err := s.PreviewPolicy(t.Context(), 1, 7, p)
	require.NoError(t, err)
	fmt.Printf("ADDED_RULE_READ calls=%d engine_mutex_held=%t\n", r.calls, r.held)
	require.False(t, r.held, "new reach path must not acquire engine mutex for settings I/O")
	require.Zero(t, r.calls, "reach reuses the candidate assessment parameters without another settings query")
}

func TestSchedAdminPolicyReachTracksStoredTolerance(t *testing.T) {
	s, _, _ := schedAdminFixture()
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{1: {ID: 1, Platform: PlatformOpenAI}}, []int64{1}}
	f := newQualityFixture(t)
	f.now = s.d.Now()
	s.d.Now = func() time.Time { return f.now }
	policy := q.DefaultPolicy()
	policy.Enabled, policy.TTFTSeconds = true, 12.5
	f.store.Members = map[int64][]int64{1: {1}}
	f.store.Policies = map[int64]SchedPolicyRecord{1: {GroupID: 1, Policy: policy}}
	facts := map[string]SchedPerfFact{}
	for i := range 20 {
		fact := perfFact(1, int64(8+i%2))
		fact.Start, fact.Finish = f.now.Add(-time.Minute).UnixMicro(), f.now.Add(-time.Second).UnixMicro()
		fact.TTFO = 12500
		if i < 4 {
			fact.TTFO = 12600
		}
		facts[fmt.Sprint(i)] = fact
	}
	published := buildSchedPerf(facts, nil, nil, nil, f.now)
	f.e.performance.Store(published)
	s.d.Engine = f.e
	preview := func(ok int, tolerance float64) {
		t.Helper()
		out, err := s.PreviewPolicy(t.Context(), 1, 0, policy)
		require.NoError(t, err)
		require.Equal(t, []SchedAdminPolicyReach{{Model: "text", FirstOutput: SchedAdminReachCount{OK: ok, N: 1}}}, out.Reach)
		_, params, err := f.e.PreviewPolicy(t.Context(), 1, policy)
		require.NoError(t, err)
		require.Equal(t, tolerance, params.Tolerance, "reach and judge use the same rule returned by the assessment")
		require.Same(t, published, f.e.PerformanceSnapshot(), "settings changes need no new performance publication")
	}
	preview(1, .20)
	// Exercise the real settings transaction, not a rule-returning admin stub.
	params := q.DefaultRuleBParams()
	params.Tolerance = .19
	saved, err := f.e.UpdateTTFTRule(t.Context(), 0, params)
	require.NoError(t, err)
	require.Equal(t, params, f.store.Rule.Params)
	preview(0, .19)
	_, copied, err := f.e.PreviewPolicy(t.Context(), 1, policy)
	require.NoError(t, err)
	params.Tolerance = .20
	_, err = f.e.UpdateTTFTRule(t.Context(), saved.Version, params)
	require.NoError(t, err)
	preview(1, .20)
	require.Equal(t, .19, copied.Tolerance, "returned rule parameters are a value copy")
	// A setting changed on another instance follows the existing config TTL.
	f.store.Rule.Params.Tolerance = .19
	ttl := min(q.MembersCacheTTL, q.PoliciesCacheTTL)
	require.LessOrEqual(t, ttl, schedPerfTick)
	f.now = f.now.Add(ttl - time.Nanosecond)
	preview(1, .20)
	f.now = f.now.Add(time.Nanosecond)
	preview(0, .19)
}
