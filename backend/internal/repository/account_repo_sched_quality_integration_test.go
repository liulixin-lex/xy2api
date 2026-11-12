//go:build integration

package repository

import (
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (s *AccountRepoSuite) TestListWithFiltersSchedQuality() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "quality", Platform: service.PlatformOpenAI})
	cooling := mustCreateAccount(s.T(), s.client, &service.Account{Name: "a-cooling", Platform: service.PlatformOpenAI})
	exempt := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-exempt", Platform: service.PlatformOpenAI})
	demote := mustCreateAccount(s.T(), s.client, &service.Account{Name: "c-demote", Platform: service.PlatformOpenAI})
	normal := mustCreateAccount(s.T(), s.client, &service.Account{Name: "d-normal", Platform: service.PlatformOpenAI})
	other := mustCreateAccount(s.T(), s.client, &service.Account{Name: "e-other", Platform: service.PlatformAnthropic})
	for _, id := range []int64{cooling.ID, demote.ID, normal.ID} {
		_, err := s.client.AccountGroup.Create().SetAccountID(id).SetGroupID(group.ID).Save(s.ctx)
		s.Require().NoError(err)
	}
	attention := service.SchedAdminAttention{Cooling: []int64{cooling.ID}, Exempt: []int64{exempt.ID}, Demote: []int64{demote.ID}}
	for _, tc := range []struct {
		name, quality string
		group         int64
		platform      string
		empty         bool
		want          []int64
	}{
		{name: "cooling", quality: "cooling", want: []int64{cooling.ID}},
		{name: "exempt_ungrouped", quality: "exempt", want: []int64{exempt.ID}},
		{name: "demote", quality: "demote", want: []int64{demote.ID}},
		{name: "normal_includes_other_platforms", quality: "normal", want: []int64{normal.ID, other.ID}},
		{name: "normal_group", quality: "normal", group: group.ID, want: []int64{normal.ID}},
		{name: "normal_platform", quality: "normal", platform: service.PlatformOpenAI, want: []int64{normal.ID}},
		{name: "exempt_group_no_match", quality: "exempt", group: group.ID, want: []int64{}},
		{name: "demote_group", quality: "demote", group: group.ID, want: []int64{demote.ID}},
		{name: "empty_cooling", quality: "cooling", empty: true, want: []int64{}},
		{name: "empty_exempt", quality: "exempt", empty: true, want: []int64{}},
		{name: "empty_demote", quality: "demote", empty: true, want: []int64{}},
		{name: "normal_empty_union", quality: "normal", empty: true, want: []int64{cooling.ID, exempt.ID, demote.ID, normal.ID, other.ID}},
	} {
		s.Run(tc.name, func() {
			value := attention
			if tc.empty {
				value = service.SchedAdminAttention{}
			}
			filter := value.Filter(tc.quality)
			ctx := service.WithSchedQualityAccountFilter(s.ctx, filter.IDs, filter.Exclude)
			rows, page, err := s.repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, tc.platform, "", "", "", tc.group, "")
			s.Require().NoError(err)
			ids := []int64{}
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
			s.Require().Equal(tc.want, ids)
			s.Require().Equal(int64(len(tc.want)), page.Total)
		})
	}
}
