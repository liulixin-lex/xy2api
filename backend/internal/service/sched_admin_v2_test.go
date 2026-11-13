//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestSchedAdminSummaryAllModelsMarksAndStatus(t *testing.T) {
	s, e, r := schedAdminFixture()
	p := e.snapshot
	now := s.d.Now()
	p.Accounts[1] = schedquality.AccountSummary{Speed: schedquality.SpeedEvidence{N: 4, P50Ms: schedAdminInt(120)}, Error: schedquality.ErrorEvidence{N: 60, Rate: .1}, Cache: schedquality.CacheEvidence{Turns: 5, InputTokens: 100, Ratio: .8}}
	p.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "a", View: schedquality.StateView{Failing: true, Dims: []schedquality.Dimension{schedquality.DimensionCache}, Until: now.Add(time.Minute)}}, {GroupID: 1, Model: "b", View: schedquality.StateView{Failing: true, Dims: []schedquality.Dimension{schedquality.DimensionError}, Until: now.Add(time.Minute)}}}
	p.Overrides = []SchedOverride{{AccountID: 1, Mode: schedquality.OverrideExempt}}
	p.ResetAt = map[int64]time.Time{1: now.Add(-time.Minute)}
	for _, id := range []int64{2, 3, 4, 5} {
		a := *r.rows[1]
		a.ID = id
		r.rows[id] = &a
	}
	r.rows[2].Schedulable = false
	r.rows[3].Platform = PlatformAnthropic
	out, err := s.Summary(context.Background(), []int64{1, 2, 3, 4, 999, 1})
	require.NoError(t, err)
	require.Len(t, out.Accounts, 4)
	a := out.Accounts[0]
	require.Equal(t, "normal", a.Status)
	require.Equal(t, 120, *a.TTFO.P50Ms)
	require.True(t, a.TTFO.Short)
	require.False(t, a.TTFO.Red)
	require.True(t, a.Error.Red)
	require.False(t, a.Error.Short)
	require.True(t, a.Cache.Red)
	require.True(t, a.Cache.Short)
	require.Len(t, a.Substandard, 2)
	require.True(t, a.Substandard[0].Exempt)
	require.Equal(t, schedAdminTime(p.ResetAt[1]), a.ResetAt)
	require.Equal(t, "disabled", out.Accounts[1].Status)
	require.Equal(t, "manual", out.Accounts[1].Stop.Source)
	require.Equal(t, "normal", out.Accounts[2].Status)
	require.Nil(t, out.Accounts[2].TTFO.P50Ms)
	require.Zero(t, out.Accounts[2].TTFO.N)
	require.Equal(t, "observing", out.Accounts[3].Status)
	attention, err := s.Attention(context.Background())
	require.NoError(t, err)
	require.Empty(t, attention.Substandard)
	require.Equal(t, []int64{1}, attention.Exempt)
	require.Equal(t, []int64{4, 5}, attention.Observing)
	p.Overrides = nil
	attention, err = s.Attention(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{1}, attention.Substandard)
}
func TestSchedAdminDetailEvidenceAndEmptyLists(t *testing.T) {
	s, e, _ := schedAdminFixture()
	p := e.snapshot
	p.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: "m", View: schedquality.StateView{Failing: true, Dims: []schedquality.Dimension{schedquality.DimensionTTFO}, Level: 2, Since: s.d.Now().Add(-time.Minute), Until: s.d.Now().Add(time.Minute)}, OtherGroupIDs: []int64{2}, Evaluation: schedquality.Evaluation{Speed: schedquality.SpeedEvidence{EvidenceWindow: schedquality.EvidenceWindow{Source: schedquality.EvidenceShared, Window: 60 * time.Minute}, N: 20, Slow: 8, LCB: .2}, Error: schedquality.ErrorEvidence{EvidenceWindow: schedquality.EvidenceWindow{Source: schedquality.EvidenceLocal, Window: 120 * time.Minute}}, Verdicts: schedquality.Verdicts{Speed: schedquality.VerdictFail}}}}
	out, err := s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, out.Groups, 1)
	m := out.Groups[0].ByModel[0]
	require.True(t, m.Failing)
	require.Equal(t, "merged", m.TTFO.Scope)
	require.Equal(t, 60, m.TTFO.WindowMin)
	require.Equal(t, 8, m.TTFO.Bad)
	require.Equal(t, "fail", m.TTFO.Verdict)
	require.Equal(t, "short", m.Error.Verdict)
	require.Nil(t, m.Error.LCB)
	require.Equal(t, []int64{2}, m.OtherGroupIDs)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"exemptions":[]`)
	require.NotContains(t, string(raw), "p90")
	require.NotContains(t, string(raw), "testing")
}

type schedAdminGroupMembers struct {
	schedAdminGroups
	ids []int64
}

func (g schedAdminGroupMembers) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return g.ids, nil
}

type schedAdminUsage struct {
	calls int
	at    time.Time
}

func (u *schedAdminUsage) GroupLast60(_ context.Context, ids []int64, at time.Time) (map[int64]usagestats.GroupLast60, error) {
	u.calls++
	u.at = at
	return map[int64]usagestats.GroupLast60{1: {Requests: 7, FailedRate: schedAdminFloat(1.0 / 7)}}, nil
}
func (e *schedAdminEngine) PreviewPolicy(_ context.Context, g int64, p schedquality.Policy) ([]SchedPolicyChange, error) {
	return e.changes, nil
}
func TestSchedAdminGroupCountsAndPreview(t *testing.T) {
	s, e, r := schedAdminFixture()
	now := s.d.Now()
	p := e.snapshot
	for id := int64(2); id <= 5; id++ {
		a := *r.rows[1]
		a.ID = id
		a.Name = "synthetic"
		a.Extra = map[string]any{}
		r.rows[id] = &a
	}
	r.rows[3].Schedulable = false
	r.rows[4].Extra["model_rate_limits"] = map[string]any{"m": map[string]any{"rate_limit_reset_at": now.Add(time.Hour).Format(time.RFC3339)}}
	s.d.Groups = schedAdminGroupMembers{schedAdminGroups{1: {ID: 1, Platform: PlatformOpenAI}}, []int64{1, 2, 3, 4, 5}}
	usage := &schedAdminUsage{}
	s.d.Usage = usage
	policy := schedquality.Policy{Enabled: true, TTFTSeconds: 10, MaxErrorRate: .05, MinCacheRatio: .5, TakeoverIdleSeconds: 900}
	p.Policies[1] = SchedPolicyRecord{GroupID: 1, Version: 7, Policy: policy}
	e.policy = p.Policies[1]
	for _, id := range []int64{1, 2, 3} {
		p.GroupRows[id] = []SchedGroupRow{{GroupID: 1, Model: "a", View: schedquality.StateView{Failing: true}}, {GroupID: 1, Model: "b", View: schedquality.StateView{Failing: true}}}
	}
	p.Overrides = []SchedOverride{{AccountID: 2, GroupID: 1, Mode: schedquality.OverrideExempt}}
	out, err := s.GroupsSummary(context.Background(), []int64{1, 1})
	require.NoError(t, err)
	require.Len(t, out.Groups, 1)
	require.Equal(t, SchedAdminCounts{Normal: 3, Below: 1, Stopped: 1}, out.Groups[0].Accounts)
	require.EqualValues(t, 7, out.Groups[0].Last60.Requests)
	require.Equal(t, 1, usage.calls)
	require.Equal(t, now, usage.at)
	e.changes = []SchedPolicyChange{{AccountID: 2, Model: "c"}, {AccountID: 3, Model: "c"}, {AccountID: 4, Model: "a", Dims: []schedquality.Dimension{schedquality.DimensionError}}, {AccountID: 4, Model: "b", Dims: []schedquality.Dimension{schedquality.DimensionCache}}}
	before := append([]SchedGroupRow(nil), p.GroupRows[1]...)
	preview, err := s.PreviewPolicy(context.Background(), 1, 7, policy)
	require.NoError(t, err)
	require.Equal(t, SchedAdminPreviewCounts{Normal: 3, Below: 1}, preview.Now)
	require.Equal(t, SchedAdminPreviewCounts{Normal: 2, Below: 2}, preview.After)
	require.Len(t, preview.Changed, 2)
	require.Equal(t, r.rows[4].Name, preview.Changed[0].Name)
	require.Equal(t, before, p.GroupRows[1], "preview must not mutate the published snapshot")
	_, err = s.PreviewPolicy(context.Background(), 1, 6, policy)
	require.ErrorIs(t, err, ErrSchedPolicyConflict)
	policy.Enabled = false
	preview, err = s.PreviewPolicy(context.Background(), 1, 7, policy)
	require.NoError(t, err)
	require.Empty(t, preview.Changed)
	require.Equal(t, SchedAdminPreviewCounts{Normal: 4}, preview.After)
}
func TestSchedAdminOrderUsesBoardFieldsAndDefaultModel(t *testing.T) {
	s, e, _ := schedAdminFixture()
	now := s.d.Now()
	e.snapshot.GroupModels = map[int64][]SchedModelCount{1: {{Model: "popular", Requests: 10}, {Model: "other", Requests: 2}}}
	board := &schedAdminBoard{rows: []SchedBoardRow{{AccountID: 1, Name: "synthetic", Rank: 1, Layer: "below", Dims: []schedquality.Dimension{schedquality.DimensionTTFO}, LoadRate: 80, LastUsedAt: &now}}}
	s.d.Board = board
	out, err := s.Order(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, "popular", board.model)
	require.Equal(t, []string{"popular", "other"}, out.Models)
	require.Equal(t, 80, out.Rows[0].LoadRate)
	require.Equal(t, "synthetic", out.Rows[0].Name)
	require.Equal(t, schedAdminTime(now), out.Rows[0].LastUsedAt)
	e.snapshot.GroupModels = nil
	board.model = ""
	out, err = s.Order(context.Background(), 1, "")
	require.NoError(t, err)
	require.Empty(t, out.Rows)
	require.Empty(t, board.model)
}
func TestSchedAdminDemoteRejected(t *testing.T) {
	s, _, _ := schedAdminFixture()
	err := s.SetOverride(context.Background(), SchedOverride{AccountID: 1, Mode: schedquality.Override(2)})
	require.Error(t, err)
}

func TestSchedAdminPreviewMatchesEngineWithoutMutation(t *testing.T) {
	f := newQualityFixture(t)
	rows := engineSamples(20, f.now.Add(-time.Minute), true)
	for i := range rows {
		value := 500
		rows[i].TTFOMs = &value
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
	expected, err := f.e.PreviewPolicy(context.Background(), 10, proposed)
	require.NoError(t, err)
	require.Len(t, expected, 1)
	decisions := len(f.store.Decisions)
	state := f.view()
	out, err := s.PreviewPolicy(context.Background(), 10, current.Version, proposed)
	require.NoError(t, err)
	require.Equal(t, SchedAdminPreviewCounts{Normal: 1}, out.Now)
	require.Equal(t, SchedAdminPreviewCounts{Below: 1}, out.After)
	require.Len(t, out.Changed, 1)
	require.Equal(t, expected[0].Dims, out.Changed[0].Dims)
	require.Equal(t, expected[0].Model, out.Changed[0].Model)
	require.Equal(t, decisions, len(f.store.Decisions))
	require.Equal(t, state, f.view())
}

func schedAdminInt(v int) *int { return &v }
