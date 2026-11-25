//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func schedAdminInt(v int) *int { return &v }

type schedAdminGroupMembers struct {
	schedAdminGroups
	ids []int64
}

func (g schedAdminGroupMembers) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return g.ids, nil
}
func (e *schedAdminEngine) PreviewPolicy(_ context.Context, _ int64, p schedquality.Policy) ([]SchedPolicyChange, error) {
	e.previewCalls = append(e.previewCalls, p)
	if e.preview != nil {
		return e.preview(p)
	}
	return e.changes, nil
}
func (r *schedAdminAccounts) SchedAdminDecisions(context.Context, int64, int, int64) ([]SchedAdminDecision, error) {
	return []SchedAdminDecision{}, nil
}
func (r *schedAdminAccounts) SchedAdminRearEntries24h(context.Context, []int64, time.Time) (map[int64]SchedAdminRearEntries, error) {
	return nil, nil
}
func (r *schedAdminAccounts) SchedAdminGroupLast60(context.Context, []int64, time.Time) (map[int64]SchedAdminGroupLast60, error) {
	return map[int64]SchedAdminGroupLast60{}, nil
}
func schedAdminJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestSchedAdminV2DetailJSONEvidence(t *testing.T) {
	s, e, _ := schedAdminFixture()
	now := s.d.Now()
	e.snapshot.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "model-a", View: schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityRear, Level: 2, Until: now.Add(time.Minute)}, Cache: schedquality.LineView{Trial: true, TrialUntil: now.Add(2 * time.Minute)}}, TTFT: schedquality.TTFTEvidence{N: 20, K: 8}, TTFTVerdict: schedquality.VerdictFail, Error: schedquality.ErrorAssessment{Verdict: schedquality.VerdictPass, SourceGroups: []int64{2}}, Cache: schedquality.CacheAssessment{SourceGroups: []int64{3}}}}
	out, err := s.Detail(context.Background(), 1)
	require.NoError(t, err)
	raw := schedAdminJSON(t, out)
	require.Equal(t, schedquality.RuleVersion, raw["current_rule_version"])
	groups := raw["groups"].([]any)
	model := groups[0].(map[string]any)["by_model"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{"model", "placement", "exempt", "lines"}, schedAdminJSONKeys(model))
	require.Equal(t, "rear", model["placement"])
	lines := model["lines"].(map[string]any)
	ttfo := lines["ttfo"].(map[string]any)
	require.Equal(t, "rear", ttfo["identity"])
	require.Equal(t, "fail", ttfo["verdict"])
	require.EqualValues(t, 2, ttfo["level"])
	evidence := ttfo["evidence"].(map[string]any)
	require.ElementsMatch(t, []string{"limit_s", "n_local", "n", "k", "x", "s", "d", "prior_mu", "p_abs", "p_rel", "u", "z", "causes"}, schedAdminJSONKeys(evidence))
	for _, key := range []string{"p_rel", "u", "z"} {
		require.Nil(t, evidence[key])
	}
	require.Equal(t, []any{}, evidence["causes"])
	require.Equal(t, []any{}, ttfo["source_groups"])
	require.Equal(t, []any{float64(2)}, lines["error"].(map[string]any)["source_groups"])
	require.Equal(t, "trial", lines["cache"].(map[string]any)["identity"])
	deadlineLine, _ := schedAdminDeadlineLine(e.snapshot.GroupRows[1][0].View, false)
	require.Equal(t, now.Add(time.Minute), deadlineLine.Until)
	require.ElementsMatch(t, []string{"limit", "clear", "failed", "unknown", "padded", "lower_fail", "lower_pass"}, schedAdminJSONKeys(lines["error"].(map[string]any)["evidence"].(map[string]any)))
	require.ElementsMatch(t, []string{"min_ratio", "turns", "ratio", "sessions", "low_sessions"}, schedAdminJSONKeys(lines["cache"].(map[string]any)["evidence"].(map[string]any)))
	require.Nil(t, e.snapshot.GroupRows[1][0].TTFT.Causes, "must not mutate the snapshot")
}
func schedAdminJSONKeys(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSchedAdminV2SummaryAndFiveFilters(t *testing.T) {
	s, e, r := schedAdminFixture()
	p := e.snapshot
	now := s.d.Now()
	p.Policies[1] = SchedPolicyRecord{Policy: schedquality.Policy{Enabled: true}}
	for id := int64(2); id <= 6; id++ {
		a := *r.rows[1]
		a.ID = id
		r.rows[id] = &a
	}
	p.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "a", View: schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityPass}}}}
	p.GroupRows[2] = []SchedGroupRow{{GroupID: 1, Model: "a", View: schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear, Until: now.Add(time.Minute)}}}}
	p.GroupRows[3] = []SchedGroupRow{{GroupID: 1, Model: "a", View: schedquality.StateView{Cache: schedquality.LineView{Trial: true, TrialUntil: now.Add(time.Minute)}}}}
	p.Overrides = []SchedOverride{{AccountID: 5, Mode: schedquality.OverrideExempt}}
	r.rows[6].Schedulable = false
	p.Accounts[1] = schedquality.AccountSummary{
		FirstOutput: schedquality.FirstOutputSummary{N: 7, Timeouts: 2, P50Ms: schedAdminInt(120)},
		Slow10s:     schedquality.Slow10sSummary{N: 6, Slow: 2, Timeouts: 1},
		ErrorN:      50, ErrorBad: 2, CacheTurns: 21, CacheRatio: schedAdminFloat(.8),
	}
	out, err := s.Summary(context.Background(), []int64{1, 2, 3, 4, 5, 6})
	require.NoError(t, err)
	item := schedAdminJSON(t, out.Accounts[0])
	require.NotContains(t, item, "status")
	require.NotContains(t, item, "substandard")
	require.NotContains(t, item, "ttfo")
	require.Equal(t, map[string]any{"n": float64(7), "timeouts": float64(2), "p50_ms": float64(120)}, item["first_output"])
	require.Equal(t, map[string]any{"n": float64(6), "slow": float64(2), "timeouts": float64(1)}, item["slow_10s"])
	require.Equal(t, map[string]any{"failed": float64(2), "n": float64(50), "rate": .04, "short": false, "red": false}, item["error"])
	require.Equal(t, map[string]any{"ratio": .8, "turns": float64(21)}, item["cache"])
	empty := schedAdminJSON(t, out.Accounts[3])
	require.Equal(t, map[string]any{"n": float64(0), "timeouts": float64(0), "p50_ms": nil}, empty["first_output"])
	require.Equal(t, map[string]any{"n": float64(0), "slow": float64(0), "timeouts": float64(0)}, empty["slow_10s"])
	require.Equal(t, map[string]any{"failed": float64(0), "n": float64(0), "rate": nil, "short": true, "red": false}, empty["error"])
	require.Equal(t, map[string]any{"ratio": nil, "turns": float64(0)}, empty["cache"])
	require.Equal(t, []any{}, item["rear"])
	require.Equal(t, []any{}, item["trial"])
	require.Equal(t, "rear", out.Accounts[1].GroupStatus["1"])
	require.True(t, out.Accounts[1].Rear[0].Effective)
	require.Equal(t, "trial", out.Accounts[2].GroupStatus["1"])
	require.Len(t, out.Accounts[2].Trial, 1)
	for _, tc := range []struct {
		quality string
		ids     []int64
	}{{"normal", []int64{1}}, {"rear", []int64{2}}, {"trial", []int64{3}}, {"unknown", []int64{4, 5, 6}}, {"exempt", []int64{5}}} {
		filter, err := s.AccountFilter(context.Background(), tc.quality)
		require.NoError(t, err)
		require.Equal(t, tc.ids, filter.IDs)
		require.True(t, ValidSchedQualityFilter(tc.quality))
	}
	require.False(t, ValidSchedQualityFilter("substandard"))
	require.False(t, ValidSchedQualityFilter("observing"))
	p.At = now.Add(-schedquality.ViewMaxAge - time.Nanosecond)
	out, err = s.Summary(context.Background(), []int64{2})
	require.NoError(t, err)
	require.False(t, out.Accounts[0].Rear[0].Effective)
	require.Equal(t, "unknown", out.Accounts[0].GroupStatus["1"])
	p.At = now
	policy := p.Policies[1]
	policy.Policy.Enabled = false
	p.Policies[1] = policy
	out, err = s.Summary(context.Background(), []int64{2})
	require.NoError(t, err)
	require.Equal(t, "unknown", out.Accounts[0].GroupStatus["1"])
	require.False(t, out.Accounts[0].Rear[0].Effective)
}

func TestSchedAdminSummaryRecentSince(t *testing.T) {
	s, e, _ := schedAdminFixture()
	// Even a stale summary describes the window at aggregation time.
	end := s.d.Now().Add(-2 * time.Hour)
	e.snapshot.At = end
	for _, tc := range []struct {
		name      string
		reset     time.Time
		shortened bool
	}{
		{"recent", end.Add(-time.Hour + time.Nanosecond), true},
		{"exactly an hour", end.Add(-time.Hour), false},
		{"older", end.Add(-2 * time.Hour), false},
		{"no reset", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.snapshot.ResetAt = map[int64]time.Time{1: tc.reset}
			e.snapshot.Accounts[1] = schedquality.SummarizeAccount(nil, 1, tc.reset, end)
			out, err := s.Summary(context.Background(), []int64{1})
			require.NoError(t, err)
			item := schedAdminJSON(t, out.Accounts[0])
			require.Contains(t, item, "recent_since")
			if tc.shortened {
				require.Equal(t, tc.reset.UTC().Format(time.RFC3339Nano), item["recent_since"])
				require.Equal(t, item["reset_at"], item["recent_since"])
			} else {
				require.Nil(t, item["recent_since"])
			}
		})
	}
}

func TestSchedAdminSummaryRecentSinceAfterRebuildWithoutAccountSamplesOrGroups(t *testing.T) {
	f := newQualityFixture(t)
	evaluatedAt := f.now
	ctx := context.Background()
	admin := NewSchedAdminService(SchedAdminDependencies{
		Engine:   f.e,
		Accounts: &schedAdminAccounts{rows: map[int64]*Account{2: {ID: 2, Platform: PlatformOpenAI}}},
		Now:      func() time.Time { return f.now },
	})

	f.now = evaluatedAt.Add(-time.Hour + time.Second)
	reset, err := f.e.Reset(ctx, 2)
	require.NoError(t, err)
	f.now = evaluatedAt
	f.cycle()

	// The reset only expires when the published aggregation window advances.
	f.now = evaluatedAt.Add(2 * time.Second)
	before, err := admin.Summary(ctx, []int64{2})
	require.NoError(t, err)
	require.Len(t, before.Accounts, 1)
	require.Equal(t, schedAdminTime(reset), before.Accounts[0].RecentSince)

	_, err = f.e.Reset(ctx, 1)
	require.NoError(t, err)
	snap := f.e.Snapshot()
	require.NotContains(t, snap.Accounts, int64(2))
	require.Equal(t, f.now, snap.Account(1).To)
	require.Equal(t, evaluatedAt, snap.At)
	after, err := admin.Summary(ctx, []int64{2})
	require.NoError(t, err)
	require.Len(t, after.Accounts, 1)
	require.Equal(t, schedAdminTime(reset), after.Accounts[0].ResetAt)
	require.Nil(t, after.Accounts[0].RecentSince)
}

type schedAdminStatsAccounts struct {
	*schedAdminAccounts
	rearErr error
	calls   int
	err     error
}

func (r *schedAdminStatsAccounts) SchedAdminRearEntries24h(context.Context, []int64, time.Time) (map[int64]SchedAdminRearEntries, error) {
	return map[int64]SchedAdminRearEntries{}, r.rearErr
}
func (r *schedAdminStatsAccounts) SchedAdminGroupLast60(_ context.Context, ids []int64, _ time.Time) (map[int64]SchedAdminGroupLast60, error) {
	r.calls++
	return map[int64]SchedAdminGroupLast60{}, r.err
}
func TestSchedAdminV2GroupsJSONAndCache(t *testing.T) {
	s, e, r := schedAdminFixture()
	now := s.d.Now()
	s.d.Now = func() time.Time { return now }
	repo := &schedAdminStatsAccounts{schedAdminAccounts: r}
	s.d.Accounts = repo
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{1: {ID: 1, Platform: PlatformOpenAI}}, []int64{1}}
	e.snapshot.Policies[1] = SchedPolicyRecord{Policy: schedquality.Policy{Enabled: true}}
	e.snapshot.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "pass", View: schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityPass}}}, {GroupID: 1, Model: "unknown"}, {GroupID: 1, Model: "trial", View: schedquality.StateView{Error: schedquality.LineView{Trial: true}}}, {GroupID: 1, Model: "rear", View: schedquality.StateView{Cache: schedquality.LineView{Identity: schedquality.IdentityRear}}}}
	out, err := s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	group := schedAdminJSON(t, out.Groups[0])
	require.ElementsMatch(t, []string{"group_id", "accounts", "last60", "rear_entries_24h"}, schedAdminJSONKeys(group))
	require.Equal(t, map[string]any{"pass": float64(1), "unknown": float64(1), "trial": float64(1), "rear": float64(1)}, group["accounts"])
	require.Equal(t, &SchedAdminRearEntries{}, out.Groups[0].RearEntries24h)
	repo.rearErr = fmt.Errorf("unavailable")
	last := group["last60"].(map[string]any)
	require.Len(t, last, 6)
	for _, v := range last {
		require.Nil(t, v)
	}
	_, err = s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, 1, repo.calls)
	now = now.Add(45 * time.Second)
	_, err = s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, repo.calls)
	policy := e.snapshot.Policies[1]
	policy.Policy.Enabled = false
	e.snapshot.Policies[1] = policy
	out, err = s.GroupsSummary(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Nil(t, out.Groups[0].RearEntries24h)
	require.Equal(t, SchedAdminCounts{Pass: 1, Unknown: 3}, out.Groups[0].Accounts)
}
func TestSchedAdminV2PreviewJSONAndConflict(t *testing.T) {
	s, e, _ := schedAdminFixture()
	policy := schedquality.DefaultPolicy()
	e.policy = SchedPolicyRecord{GroupID: 1, Version: 7, Policy: policy}
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{1: {ID: 1, Platform: PlatformOpenAI}}, []int64{1}}
	e.snapshot.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "a", TTFTVerdict: schedquality.VerdictPass}}
	e.changes = []SchedPolicyChange{{AccountID: 1, Model: "a", Lines: map[schedquality.Dimension]schedquality.Verdict{schedquality.DimensionTTFO: schedquality.VerdictFail}}}
	saved := policy
	policy.TTFTSeconds *= 2
	e.preview = func(p schedquality.Policy) ([]SchedPolicyChange, error) {
		ttfo := schedquality.VerdictPass
		if p.TTFTSeconds > saved.TTFTSeconds {
			ttfo = schedquality.VerdictFail
		}
		return []SchedPolicyChange{{AccountID: 1, Model: "a", Lines: map[schedquality.Dimension]schedquality.Verdict{schedquality.DimensionTTFO: ttfo, schedquality.DimensionError: schedquality.VerdictPass}}}, nil
	}
	reads := e.reads
	out, err := s.PreviewPolicy(context.Background(), 1, 7, policy)
	require.NoError(t, err)
	raw := schedAdminJSON(t, out)
	require.ElementsMatch(t, []string{"relaxed_lines", "lines", "changed"}, schedAdminJSONKeys(raw))
	require.Equal(t, []any{"ttfo"}, raw["relaxed_lines"])
	require.Equal(t, SchedAdminVerdictCounts{Pass: 1}, out.Lines[schedquality.DimensionTTFO].Now)
	require.Equal(t, SchedAdminVerdictCounts{Fail: 1}, out.Lines[schedquality.DimensionTTFO].After)
	require.Equal(t, []any{map[string]any{"account_id": float64(1), "name": "one", "model": "a", "line": "ttfo", "from": "pass", "to": "fail"}}, raw["changed"])
	_, err = s.PreviewPolicy(context.Background(), 1, 6, policy)
	require.ErrorIs(t, err, ErrSchedPolicyConflict)
	require.Equal(t, schedquality.VerdictPass, e.snapshot.GroupRows[1][0].TTFTVerdict)
	require.Equal(t, reads, e.reads, "preview must not read old published conclusions")
	require.Equal(t, []schedquality.Policy{saved, policy}, e.previewCalls)
	require.Equal(t, SchedAdminVerdictCounts{Pass: 1}, out.Lines[schedquality.DimensionError].Now)
	require.Equal(t, out.Lines[schedquality.DimensionError].Now, out.Lines[schedquality.DimensionError].After)
}
func TestSchedAdminV2OrderJSON(t *testing.T) {
	s, e, _ := schedAdminFixture()
	now := s.d.Now()
	e.snapshot.GroupModels = map[int64][]SchedModelCount{1: {{Model: "a", Requests: 10}}}
	// The admin snapshot deliberately disagrees: a row's layer and lines must both come from select.
	e.snapshot.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "a", View: schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityRear}}}}
	rearUntil, trialUntil, stopUntil := now.Add(20*time.Minute), now.Add(schedquality.TrialDuration), now.Add(2*time.Minute)
	board := &schedAdminBoard{rows: []SchedBoardRow{
		{AccountID: 1, Layer: "front", Rank: 1, Pinned: true, Lines: map[string]string{"ttfo": "pass", "error": "unknown", "cache": "pass"}},
		{AccountID: 2, Layer: "trial", Rank: 2, Until: &trialUntil, Lines: map[string]string{"ttfo": "trial", "error": "pass", "cache": "unknown"}},
		{AccountID: 3, Layer: "trial_full", Rank: 3, Until: &trialUntil, Lines: map[string]string{"ttfo": "unknown", "error": "trial", "cache": "pass"}},
		{AccountID: 4, Layer: "rear", Rank: 4, Until: &rearUntil, Lines: map[string]string{"ttfo": "rear", "error": "pass", "cache": "trial"}},
		{AccountID: 5, Layer: "stopped", Rank: 5, Until: &stopUntil, Excluded: "profit_floor", Lines: map[string]string{"ttfo": "unknown", "error": "unknown", "cache": "unknown"}},
	}}
	s.d.Board = board
	out, err := s.Order(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, "a", board.model)
	require.Len(t, out.Rows, 5)
	for i, source := range board.rows {
		row := schedAdminJSON(t, out.Rows[i])
		require.NotContains(t, row, "dims")
		require.Equal(t, source.Layer, row["layer"])
		require.Equal(t, source.Pinned, row["pinned"])
		require.Equal(t, map[string]any{"ttfo": source.Lines["ttfo"], "error": source.Lines["error"], "cache": source.Lines["cache"]}, row["lines"])
		if source.Until == nil {
			require.Nil(t, row["until"])
		} else {
			require.Equal(t, source.Until.UTC().Format(time.RFC3339Nano), row["until"])
		}
		if source.Excluded == "" {
			require.Nil(t, row["excluded"])
		} else {
			require.Equal(t, "profit_floor", row["excluded"])
		}
	}
	require.Equal(t, "pass", out.Rows[0].Lines["ttfo"], "do not recompute lines from another snapshot")
}

func TestSchedAdminEffectiveMatchesSelectionAuto(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		global, group bool
		age           time.Duration
		want          bool
	}{
		{"on", true, true, 0, true}, {"global off", false, true, 0, false}, {"group off", true, false, 0, false},
		{"fresh boundary", true, true, schedquality.ViewMaxAge, true}, {"stale", true, true, schedquality.ViewMaxAge + time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &SchedQualitySnapshot{At: now.Add(-tc.age), GlobalEnabled: tc.global}
			view := &schedQualityView{at: p.At, globalEnabled: tc.global, policies: map[int64]SchedPolicyRecord{1: {Policy: schedquality.Policy{Enabled: tc.group}}}}
			require.Equal(t, tc.want, selectionAuto(view, 1, now))
			require.Equal(t, selectionAuto(view, 1, now), schedAdminEffective(p, now, tc.group))
		})
	}
}

func TestSchedAdminV2PreviewUsesEngineAndDoesNotChangeIdentity(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.ledger.Rows = f.ledger.Rows[80:]
	rows := engineSamples(80, f.now.Add(-time.Minute), true)
	for i := range rows {
		value := 500
		rows[i].TTFOMs = &value
		rows[i].SessionKey = fmt.Sprintf("preview-%d", i%3)
	}
	require.NoError(t, f.ledger.InsertBatch(context.Background(), rows))
	f.now = f.now.Add(time.Minute)
	f.cycle()
	s, _, r := schedAdminFixture()
	s.d.Engine = f.e
	s.d.Now = func() time.Time { return f.now }
	r.rows[1].GroupIDs = []int64{10}
	r.rows[1].Groups = []*Group{{ID: 10, Platform: PlatformOpenAI}}
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{10: {ID: 10, Platform: PlatformOpenAI}}, []int64{1}}
	current, err := f.e.Policy(context.Background(), 10)
	require.NoError(t, err)
	proposed := current.Policy
	proposed.TTFTSeconds = .1
	before := f.view()
	decisions := len(f.store.Decisions)
	out, err := s.PreviewPolicy(context.Background(), 10, current.Version, proposed)
	require.NoError(t, err)
	require.NotEmpty(t, out.Changed)
	require.Equal(t, 0, out.Lines[schedquality.DimensionTTFO].Now.Fail)
	require.Positive(t, out.Lines[schedquality.DimensionTTFO].After.Fail)
	require.Empty(t, out.RelaxedLines)
	require.Equal(t, before, f.view())
	require.Len(t, f.store.Decisions, decisions)
}

func TestSchedAdminV2PolicySavedJSONAndChangeLog(t *testing.T) {
	s, e, _ := schedAdminFixture()
	policy := schedquality.DefaultPolicy()
	e.policy = SchedPolicyRecord{GroupID: 1, Version: 7, Policy: policy}
	policy.TTFTSeconds *= 2
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	out, err := s.UpdatePolicy(context.Background(), 1, 7, policy, 42)
	require.NoError(t, err)
	raw := schedAdminJSON(t, out)
	require.ElementsMatch(t, []string{"group_id", "version", "policy", "effective_max_error_rate", "error_trip_examples", "relaxed_lines", "cleared"}, schedAdminJSONKeys(raw))
	require.Equal(t, []any{"ttfo"}, raw["relaxed_lines"])
	require.EqualValues(t, 8, raw["version"])
	var event map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &event))
	require.Equal(t, "sched_quality.policy_changed", event["msg"])
	require.EqualValues(t, 42, event["operator_id"])
	require.Contains(t, event, "old")
	require.Contains(t, event, "new")
	require.Equal(t, []any{"ttfo"}, event["affected_lines"])
	require.EqualValues(t, 0, event["cleared"])
}

func TestSchedAdminV2RuleRejectsNonfiniteBeforeEngine(t *testing.T) {
	s, _, _ := schedAdminFixture()
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"confidence", "tolerance", "peer_margin"} {
			p := schedquality.DefaultRuleBParams()
			switch field {
			case "confidence":
				p.Confidence = v
			case "tolerance":
				p.Tolerance = v
			case "peer_margin":
				p.PeerMargin = v
			}
			_, err := s.UpdateTTFTRule(context.Background(), 7, p, 42)
			require.Error(t, err)
		}
	}
}

func TestSchedAdminTrialSummaryUsesEffectiveIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		global, group, stale, exempt bool
		wantTrial                    bool
	}{
		{name: "active", global: true, group: true, wantTrial: true},
		{name: "global off", global: false, group: true},
		{name: "group off", global: true, group: false},
		{name: "stale", global: true, group: true, stale: true},
		{name: "exempt", global: true, group: true, exempt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, _ := schedAdminFixture()
			now := s.d.Now()
			p := e.snapshot
			p.GlobalEnabled = tc.global
			p.Policies[1] = SchedPolicyRecord{Policy: schedquality.Policy{Enabled: tc.group}}
			if tc.stale {
				p.At = now.Add(-schedquality.ViewMaxAge - time.Nanosecond)
			}
			if tc.exempt {
				p.Overrides = []SchedOverride{{AccountID: 1, GroupID: 1, Mode: schedquality.OverrideExempt}}
			}
			until := now.Add(schedquality.TrialDuration)
			p.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "trial", View: schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: until}}}}
			out, err := s.Summary(context.Background(), []int64{1})
			require.NoError(t, err)
			item := out.Accounts[0]
			raw := schedAdminJSON(t, item)
			if tc.wantTrial {
				require.Equal(t, "trial", item.GroupStatus["1"])
				require.Len(t, item.Trial, 1)
				require.Equal(t, until.Format(time.RFC3339Nano), item.Trial[0].Until)
			} else {
				require.Equal(t, "unknown", item.GroupStatus["1"])
				require.Equal(t, []any{}, raw["trial"])
			}
			detail, err := s.Detail(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, "trial", detail.Groups[0].ByModel[0].Lines.TTFO.Identity, "panel retains raw identity")
		})
	}
}

func TestSchedAdminBoardSharesEngineTrialLeases(t *testing.T) {
	now := time.Now()
	leases := newSelectionTrialLeaseFake()
	leases.full[1] = true
	engine := &SchedQualityEngineService{}
	engine.SetTrialLeases(leases)
	view := selectionTestView(now)
	view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: now.Add(schedquality.TrialDuration)}}
	engine.snapshot.Store(&SchedQualitySnapshot{At: now, GlobalEnabled: true, View: view, GroupModels: map[int64][]SchedModelCount{7: {{Model: "m", Requests: 1}}}})
	gateway := &OpenAIGatewayService{accountRepo: &selectionBoardRepo{accounts: []Account{*selectionTestAccount(1, 1)}}}
	board := ProvideOpenAISelectionBoard(gateway, engine)
	board.now = func() time.Time { return now }
	admin := NewSchedAdminService(SchedAdminDependencies{Engine: engine, Board: board, Groups: schedAdminGroups{7: {ID: 7, Platform: PlatformOpenAI}}, Now: func() time.Time { return now }})
	require.Same(t, leases, engine.leases)
	require.Same(t, leases, board.leases)
	require.Same(t, board, gateway.selectionBoard)
	require.Same(t, board, admin.d.Board)
	out, err := admin.Order(context.Background(), 7, "m")
	require.NoError(t, err)
	require.Len(t, out.Rows, 1)
	require.Equal(t, "trial_full", out.Rows[0].Layer)
	leases.full[1] = false
	out, err = admin.Order(context.Background(), 7, "m")
	require.NoError(t, err)
	require.Equal(t, "trial", out.Rows[0].Layer)
	require.Equal(t, []string{"peek_room", "peek_room"}, leases.calls, "board must only inspect capacity")
	require.Empty(t, leases.held)
}

func TestObserveGroupSupplyOutsideRecentModels(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	members := make([]Account, 5)
	for i := range members {
		members[i] = *selectionTestAccount(int64(i+1), i)
		members[i].Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "target"}}
	}
	members[0].Status = StatusDisabled
	members[1].Schedulable = false
	members[2].RateLimitResetAt = &future
	members[4].Credentials = map[string]any{"model_mapping": map[string]any{"unrelated": "target"}}
	repo := &selectionBoardRepo{accounts: members}
	s, engine, _ := schedAdminFixture()
	engine.snapshot.GroupModels = map[int64][]SchedModelCount{7: {{Model: "recent"}}}
	s.d.Groups = schedAdminGroups{7: {ID: 7, Platform: PlatformOpenAI}}
	board := NewOpenAISelectionBoard(&OpenAIGatewayService{accountRepo: repo}, nil)
	board.now = func() time.Time { return now }
	s.d.Board = board
	out, err := s.Order(ctx, 7, "other-model")
	require.NoError(t, err)
	require.Equal(t, []string{"recent"}, out.Models)
	require.Equal(t, "other-model", out.Model)
	require.Equal(t, &SchedAdminSupply{Declared: 4, Enabled: 2, Available: 1, At: schedAdminStamp(now)}, out.Supply)

	repo.accounts = members[:2]
	out, err = s.Order(ctx, 7, "other-model")
	require.NoError(t, err)
	require.Equal(t, &SchedAdminSupply{Declared: 2, At: schedAdminStamp(now)}, out.Supply)
	repo.accounts = nil
	out, err = s.Order(ctx, 7, "other-model")
	require.NoError(t, err)
	require.Empty(t, out.Rows)
	require.Equal(t, &SchedAdminSupply{At: schedAdminStamp(now)}, out.Supply)
}
