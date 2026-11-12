//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedAdminSummaryActiveOverrides(t *testing.T) {
	s, e, _ := schedAdminFixture()
	p, now := e.snapshot, s.d.Now()
	expired, future := now.Add(-time.Second), now.Add(time.Hour)
	p.Members = map[int64][]int64{1: {4, 3, 2, 1}}
	p.Overrides = []SchedOverride{
		{AccountID: 1, GroupID: 4, Mode: schedquality.OverrideDemote, ExpiresAt: &future, Reason: "private", OperatorID: 42},
		{AccountID: 1, GroupID: 2, Mode: schedquality.OverrideDemote, ExpiresAt: &now},
		{AccountID: 1, GroupID: 0, Mode: schedquality.OverrideExempt},
		{AccountID: 1, GroupID: 3, Mode: schedquality.OverrideDemote, ExpiresAt: &expired},
		{AccountID: 1, GroupID: 1, Mode: schedquality.OverrideDemote},
		{AccountID: 1, GroupID: 99, Mode: schedquality.OverrideExempt},
		{AccountID: 2, GroupID: 0, Mode: schedquality.OverrideDemote},
	}
	out, err := s.Summary(context.Background(), []int64{1, 2, 3}, "")
	require.NoError(t, err)
	require.Equal(t, []SchedAdminAccountSummaryOverride{
		{GroupID: 0, Mode: "exempt"}, {GroupID: 1, Mode: "demote"}, {GroupID: 4, Mode: "demote", ExpiresAt: schedAdminTime(future)},
	}, out.Accounts[0].Overrides)
	require.Equal(t, []SchedAdminAccountSummaryOverride{{GroupID: 0, Mode: "demote"}}, out.Accounts[1].Overrides)
	require.Equal(t, []SchedAdminAccountSummaryOverride{}, out.Accounts[2].Overrides)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "reason\":\"private")
	require.NotContains(t, string(encoded), "operator")
	for _, group := range []int64{2, 3} {
		effective := s.override(context.Background(), p, 1, group, now)
		require.NotNil(t, effective)
		require.True(t, effective.AllGroups)
		require.Equal(t, "exempt", effective.Mode)
	}
}

func TestSchedAdminSummaryAllCoolingGroupsPreferRunningModel(t *testing.T) {
	s, e, _ := schedAdminFixture()
	p := e.snapshot
	p.Members = map[int64][]int64{1: {1, 2, 3, 4, 5, 6}}
	for g := int64(5); g > 0; g-- {
		p.GroupRows[1] = append(p.GroupRows[1], SchedGroupRow{GroupID: g, Model: "a", View: schedquality.StateView{State: schedquality.StateCooling, Reason: schedquality.ReasonSpeed}})
	}
	until := s.d.Now().Add(time.Minute)
	p.GroupRows[1] = append(p.GroupRows[1], SchedGroupRow{GroupID: 1, Model: "z", View: schedquality.StateView{State: schedquality.StateCooling, Reason: schedquality.ReasonTestError, Until: until, WaitingTest: true}}, SchedGroupRow{GroupID: 6, Model: "z"})
	p.Tests[SchedAccountModel{1, "z"}] = SchedTestRecord{Running: true}
	out, err := s.Summary(context.Background(), []int64{1}, "")
	require.NoError(t, err)
	require.Len(t, out.Accounts[0].Cooling, 5)
	require.Zero(t, out.Accounts[0].CoolingMore)
	for i, cooling := range out.Accounts[0].Cooling {
		require.Equal(t, int64(i+1), cooling.GroupID)
	}
	require.Equal(t, SchedAdminAccountSummaryCooling{GroupID: 1, Model: "z", Reason: "test_error", Until: schedAdminTime(until), WaitingTest: true, Testing: true}, out.Accounts[0].Cooling[0])
	require.Equal(t, "a", out.Accounts[0].Cooling[1].Model)
	require.False(t, out.Accounts[0].Cooling[1].Testing)
	p.Tests[SchedAccountModel{1, "z"}] = SchedTestRecord{}
	out, err = s.Summary(context.Background(), []int64{1}, "")
	require.NoError(t, err)
	require.Equal(t, "a", out.Accounts[0].Cooling[0].Model)
}

func TestSchedAdminAttentionActiveOverrideRecords(t *testing.T) {
	s, e, _ := schedAdminFixture()
	p := e.snapshot
	now := s.d.Now()
	p.Members = map[int64][]int64{1: {10}, 3: {10}, 4: {10, 20}, 5: {10}, 6: {10}}
	p.GroupRows[1] = []SchedGroupRow{{GroupID: 10, View: schedquality.StateView{State: schedquality.StateCooling}}}
	p.GroupRows[5] = []SchedGroupRow{{GroupID: 99, View: schedquality.StateView{State: schedquality.StateCooling}}}
	p.Overrides = []SchedOverride{
		{AccountID: 1, Mode: schedquality.OverrideExempt},
		{AccountID: 1, GroupID: 10, Mode: schedquality.OverrideDemote},
		{AccountID: 2, Mode: schedquality.OverrideExempt},
		{AccountID: 3, Mode: schedquality.OverrideExempt},
		{AccountID: 3, GroupID: 10, Mode: schedquality.OverrideDemote, ExpiresAt: &now},
		{AccountID: 4, Mode: schedquality.OverrideExempt},
		{AccountID: 4, GroupID: 10, Mode: schedquality.OverrideDemote},
		{AccountID: 5, GroupID: 99, Mode: schedquality.OverrideExempt},
		{AccountID: 6, Mode: schedquality.OverrideDemote, ExpiresAt: &now},
	}
	out, err := s.Attention(context.Background())
	require.NoError(t, err)
	require.Equal(t, SchedAdminAttention{Cooling: []int64{1}, Exempt: []int64{1, 2, 3, 4}, Demote: []int64{1, 4}}, out)
	require.Contains(t, out.Filter("normal").IDs, int64(1))
	require.True(t, out.Filter("normal").Exclude)
	e.snapshot.Overrides = nil
	e.snapshot.GroupRows = nil
	out, err = s.Attention(context.Background())
	require.NoError(t, err)
	require.Equal(t, SchedAdminAttention{Cooling: []int64{}, Exempt: []int64{}, Demote: []int64{}}, out)
}

func TestSchedAdminDetailGroupsWithoutState(t *testing.T) {
	s, e, repo := schedAdminFixture()
	p := e.snapshot
	repo.rows[1].GroupIDs = []int64{3, 2, 1}
	s.d.Groups = schedAdminGroups{1: {ID: 1, Name: "one", Platform: PlatformOpenAI}, 2: {ID: 2, Name: "two", Platform: PlatformOpenAI}, 3: {ID: 3, Platform: PlatformAnthropic}}
	p.Overrides = []SchedOverride{{AccountID: 1, Mode: schedquality.OverrideExempt, Reason: "all"}, {AccountID: 1, GroupID: 2, Mode: schedquality.OverrideDemote}}
	p.Policies[2] = SchedPolicyRecord{Policy: schedquality.Policy{Enabled: true, TTFTSeconds: 10}}
	out, err := s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, out.Groups, 2)
	require.Equal(t, &SchedAdminOverride{Mode: "exempt", AllGroups: true, Reason: "all"}, out.AllGroupsOverride)
	for i, g := range out.Groups {
		require.Equal(t, int64(i+1), g.GroupID)
		require.NotNil(t, g.ByModel)
		require.Empty(t, g.ByModel)
	}
	require.True(t, out.Groups[0].Override.AllGroups)
	require.Equal(t, "demote", out.Groups[1].Override.Mode)
	require.True(t, out.Groups[1].GateEnabled)
	repo.rows[1].GroupIDs = nil
	out, err = s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, out.Groups)
	require.NotNil(t, out.AllGroupsOverride)
	e.overrides = append([]SchedOverride(nil), p.Overrides...)
	require.NoError(t, s.ClearOverride(context.Background(), 1, 0, 7))
	p.Overrides = e.overrides
	out, err = s.Detail(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, out.AllGroupsOverride)
}

func TestSchedAdminDetailTestKind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		auto  bool
		tiers []schedquality.TestTier
		kind  string
	}{
		{"screen", true, []schedquality.TestTier{schedquality.TierSmall}, "screen"},
		{"empty_auto", true, nil, "screen"},
		{"old_auto", true, []schedquality.TestTier{schedquality.TierSmall, schedquality.TierMedium, schedquality.TierLarge}, "full"},
		{"medium_auto", true, []schedquality.TestTier{schedquality.TierMedium}, "full"},
		{"large_auto", true, []schedquality.TestTier{schedquality.TierLarge}, "full"},
		{"partial_manual", false, []schedquality.TestTier{schedquality.TierSmall}, "full"},
		{"empty_manual", false, nil, "full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := SchedTestRecord{Auto: tc.auto}
			for _, tier := range tc.tiers {
				record.Turns = append(record.Turns, schedquality.TestTurn{Tier: tier, Turn: 1, Outcome: schedquality.OutcomeOK})
			}
			require.Equal(t, tc.kind, schedAdminTestDetail(record).Kind)
		})
	}
}
