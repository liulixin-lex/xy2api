//go:build integration

package repository

import (
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (s *AccountRepoSuite) TestListWithFiltersSchedQuality() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "quality", Platform: service.PlatformOpenAI})
	substandard := mustCreateAccount(s.T(), s.client, &service.Account{Name: "a-substandard", Platform: service.PlatformOpenAI})
	exempt := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-exempt", Platform: service.PlatformOpenAI})
	observing := mustCreateAccount(s.T(), s.client, &service.Account{Name: "c-observing", Platform: service.PlatformOpenAI})
	normal := mustCreateAccount(s.T(), s.client, &service.Account{Name: "d-normal", Platform: service.PlatformOpenAI})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "e-other", Platform: service.PlatformAnthropic})
	for _, id := range []int64{substandard.ID, observing.ID, normal.ID} {
		_, err := s.client.AccountGroup.Create().SetAccountID(id).SetGroupID(group.ID).Save(s.ctx)
		s.Require().NoError(err)
	}
	attention := service.SchedAdminAttention{Rear: []int64{substandard.ID}, Exempt: []int64{exempt.ID}, Unknown: []int64{observing.ID}}
	normalIDs := []int64{normal.ID}
	for _, tc := range []struct {
		name, quality string
		group         int64
		platform      string
		empty         bool
		want          []int64
	}{
		{name: "rear", quality: "rear", want: []int64{substandard.ID}},
		{name: "exempt_ungrouped", quality: "exempt", want: []int64{exempt.ID}},
		{name: "unknown", quality: "unknown", want: []int64{observing.ID}},
		{name: "normal_excludes_other_platforms", quality: "normal", want: []int64{normal.ID}},
		{name: "normal_group", quality: "normal", group: group.ID, want: []int64{normal.ID}},
		{name: "normal_platform", quality: "normal", platform: service.PlatformOpenAI, want: []int64{normal.ID}},
		{name: "exempt_group_no_match", quality: "exempt", group: group.ID, want: []int64{}},
		{name: "observing_group", quality: "unknown", group: group.ID, want: []int64{observing.ID}},
		{name: "empty_substandard", quality: "rear", empty: true, want: []int64{}},
		{name: "empty_exempt", quality: "exempt", empty: true, want: []int64{}},
		{name: "empty_observing", quality: "unknown", empty: true, want: []int64{}},
		{name: "normal_empty", quality: "normal", empty: true, want: []int64{}},
	} {
		s.Run(tc.name, func() {
			value := attention
			eligible := normalIDs
			if tc.empty {
				value = service.SchedAdminAttention{}
				eligible = nil
			}
			filter := value.Filter(tc.quality, eligible)
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
