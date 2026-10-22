//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type accountRepoCategoryCapture struct {
	accountRepoStubForBulkUpdate
	listCalls         int
	accountCategoryID int64
	groupListCategory int64
	iqStatus          string
}

func (s *accountRepoCategoryCapture) ListWithFilters(ctx context.Context, _ pagination.PaginationParams, _, _, _, _ string, _ int64, _ string) ([]Account, *pagination.PaginationResult, error) {
	s.listCalls++
	s.accountCategoryID = AccountGroupCategoryFilter(ctx)
	s.groupListCategory = GroupListCategoryFilter(ctx)
	s.iqStatus = IQStatusFilter(ctx)
	return []Account{}, &pagination.PaginationResult{Total: 0}, nil
}

// 按筛选批量编辑时，group_category 和 IQ 状态一样经上下文到达账号仓储。
func TestBulkUpdateFiltersCarryGroupCategoryToAccountList(t *testing.T) {
	repo := &accountRepoCategoryCapture{}
	svc := &adminServiceImpl{accountRepo: repo}
	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		Filters: &BulkUpdateAccountFilters{Category: "3", IQStatus: "smart"},
	})
	require.NoError(t, err)
	require.Empty(t, result.SuccessIDs)
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, int64(3), repo.accountCategoryID)
	require.Zero(t, repo.groupListCategory, "the account filter never reaches the group list key")
	require.Equal(t, "smart", repo.iqStatus)

	repo = &accountRepoCategoryCapture{}
	svc = &adminServiceImpl{accountRepo: repo}
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{Filters: &BulkUpdateAccountFilters{}})
	require.NoError(t, err)
	require.Zero(t, repo.accountCategoryID)

	repo = &accountRepoCategoryCapture{}
	svc = &adminServiceImpl{accountRepo: repo}
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{Filters: &BulkUpdateAccountFilters{Category: "x"}})
	requireApplicationErrorReason(t, err, "INVALID_GROUP_CATEGORY_FILTER")
	require.Zero(t, repo.listCalls, "a bad filter never selects targets")
}
