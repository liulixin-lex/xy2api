//go:build integration

package repository

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 分类表只有这些测试写入；每个测试开始和结束时清空分类，并删除自己建的分组和账号。
func resetGroupCategoryTables(t *testing.T) {
	t.Helper()
	_, err := integrationDB.Exec(`DELETE FROM group_categories`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM group_categories`) })
}

func groupCategoryTestPrefix() string {
	return fmt.Sprintf("gcat%d-", time.Now().UnixNano())
}

func mustCreateCategoryGroup(t *testing.T, name string) *service.Group {
	t.Helper()
	g := mustCreateGroup(t, testEntClient(t), &service.Group{Name: name, RateMultiplier: 1})
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM account_groups WHERE group_id = $1`, g.ID)
		_, _ = integrationDB.Exec(`DELETE FROM groups WHERE id = $1`, g.ID)
	})
	return g
}

func mustCreateCategoryAccount(t *testing.T, name string, groupIDs ...int64) *service.Account {
	t.Helper()
	a := mustCreateAccount(t, testEntClient(t), &service.Account{Name: name})
	for i, groupID := range groupIDs {
		mustBindAccountToGroup(t, testEntClient(t), a.ID, groupID, i+1)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM account_groups WHERE account_id = $1`, a.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id = $1`, a.ID)
	})
	return a
}

func softDeleteCategoryGroup(t *testing.T, groupID int64) {
	t.Helper()
	_, err := integrationDB.Exec(`UPDATE groups SET deleted_at = NOW() WHERE id = $1`, groupID)
	require.NoError(t, err)
}

func categoryByName(categories []service.GroupCategory) map[string]service.GroupCategory {
	out := make(map[string]service.GroupCategory, len(categories))
	for _, category := range categories {
		out[category.Name] = category
	}
	return out
}

func categoryNames(categories []service.GroupCategory) []string {
	out := make([]string, 0, len(categories))
	for _, category := range categories {
		out = append(out, category.Name)
	}
	return out
}

func groupCategoryMemberIDs(t *testing.T, groupID int64) []int64 {
	t.Helper()
	ids, err := queryInt64s(context.Background(), integrationDB, `SELECT category_id FROM group_category_members WHERE group_id = $1 ORDER BY category_id`, groupID)
	require.NoError(t, err)
	return ids
}

func TestGroupCategorySaveReconcilesRenameReorderDelete(t *testing.T) {
	resetGroupCategoryTables(t)
	ctx := context.Background()
	repo := NewGroupCategoryRepository(integrationDB)
	g := mustCreateCategoryGroup(t, groupCategoryTestPrefix()+"g")

	created, err := repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{Name: "A"}, {Name: "B"}, {Name: "C"}}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C"}, categoryNames(created))
	byName := categoryByName(created)
	a, b, c := byName["A"].ID, byName["B"].ID, byName["C"].ID
	_, err = repo.SetGroupCategories(ctx, g.ID, []int64{b})
	require.NoError(t, err)

	// 另一位管理员同时新建的分类：不在 items 也不在 deleted_ids 里。
	var direct int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO group_categories (name, sort_order) VALUES ('Direct', 0) RETURNING id`).Scan(&direct))

	saved, err := repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{ID: c, Name: "C2"}, {ID: a, Name: "A"}, {Name: "D"}}, []int64{b})
	require.NoError(t, err)
	require.Equal(t, []string{"C2", "A", "D", "Direct"}, categoryNames(saved), "saved items in order, then the unlisted category")
	for i, category := range saved {
		require.Equal(t, i, category.SortOrder)
		require.NotNil(t, category.GroupIDs)
	}
	byName = categoryByName(saved)
	require.Equal(t, c, byName["C2"].ID)
	require.Equal(t, a, byName["A"].ID)
	require.Equal(t, direct, byName["Direct"].ID)
	require.Greater(t, byName["D"].ID, direct, "a new category never reuses a deleted id")
	require.Empty(t, groupCategoryMemberIDs(t, g.ID), "deleting a category removes it from its groups")

	// 一次保存里互换名称是允许的。
	swapped, err := repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{ID: c, Name: "A"}, {ID: a, Name: "C2"}}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "C2", "D", "Direct"}, categoryNames(swapped))
	require.Equal(t, c, swapped[0].ID)

	// 已被删除的分类直接跳过。
	skipped, err := repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{ID: b, Name: "ghost"}}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "C2", "D", "Direct"}, categoryNames(skipped))

	_, err = repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{Name: "direct"}}, nil)
	require.ErrorIs(t, err, service.ErrGroupCategoryNameDuplicate, "an unlisted category's name counts too")
	_, err = repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{ID: c, Name: "Same"}, {Name: "same"}}, nil)
	require.ErrorIs(t, err, service.ErrGroupCategoryNameDuplicate)

	many := make([]service.GroupCategoryDraft, 0, service.MaxGroupCategories)
	for i := 0; i < service.MaxGroupCategories-3; i++ {
		many = append(many, service.GroupCategoryDraft{Name: "n" + strconv.Itoa(i)})
	}
	_, err = repo.SaveGroupCategories(ctx, many, nil)
	require.ErrorIs(t, err, service.ErrGroupCategoryLimit, "47 new plus 4 kept is over 50")

	after, err := repo.ListGroupCategories(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "C2", "D", "Direct"}, categoryNames(after), "rejected saves change nothing")
}

func TestSetGroupCategoriesReplacesAndDropsUnknown(t *testing.T) {
	resetGroupCategoryTables(t)
	ctx := context.Background()
	repo := NewGroupCategoryRepository(integrationDB)
	prefix := groupCategoryTestPrefix()
	g := mustCreateCategoryGroup(t, prefix+"g")
	gone := mustCreateCategoryGroup(t, prefix+"gone")

	created, err := repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{Name: "X"}, {Name: "Y"}, {Name: "Z"}}, nil)
	require.NoError(t, err)
	byName := categoryByName(created)
	x, y, z := byName["X"].ID, byName["Y"].ID, byName["Z"].ID

	ids, err := repo.SetGroupCategories(ctx, g.ID, []int64{z, x, 99999999})
	require.NoError(t, err)
	require.Equal(t, []int64{x, z}, ids, "unknown ids are dropped; the answer follows the category order")
	require.ElementsMatch(t, []int64{x, z}, groupCategoryMemberIDs(t, g.ID))

	for i := 0; i < 2; i++ {
		ids, err = repo.SetGroupCategories(ctx, g.ID, []int64{y})
		require.NoError(t, err)
		require.Equal(t, []int64{y}, ids)
		require.Equal(t, []int64{y}, groupCategoryMemberIDs(t, g.ID))
	}
	ids, err = repo.SetGroupCategories(ctx, g.ID, []int64{})
	require.NoError(t, err)
	require.Empty(t, ids)
	require.Empty(t, groupCategoryMemberIDs(t, g.ID))

	_, err = repo.SetGroupCategories(ctx, g.ID, []int64{x})
	require.NoError(t, err)
	_, err = repo.SetGroupCategories(ctx, gone.ID, []int64{x})
	require.NoError(t, err)
	softDeleteCategoryGroup(t, gone.ID)
	list, err := repo.ListGroupCategories(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{g.ID}, categoryByName(list)["X"].GroupIDs, "soft-deleted groups are skipped on read")
	require.Equal(t, []int64{x}, groupCategoryMemberIDs(t, gone.ID), "member rows of a soft-deleted group are kept")

	_, err = repo.SetGroupCategories(ctx, gone.ID, []int64{y})
	require.ErrorIs(t, err, service.ErrGroupNotFound)
	_, err = repo.SetGroupCategories(ctx, 99999999, []int64{y})
	require.ErrorIs(t, err, service.ErrGroupNotFound)
}

// TestGroupCategoryFiltersNarrowGroupAndAccountLists ?group_category 经各自的 context key 收窄
// 分组列表和账号列表；调度评分池用的 ListAllWithFilters 不跟随。
func TestGroupCategoryFiltersNarrowGroupAndAccountLists(t *testing.T) {
	resetGroupCategoryTables(t)
	ctx := context.Background()
	catRepo := NewGroupCategoryRepository(integrationDB)
	groupRepo := newGroupRepositoryWithSQL(testEntClient(t), integrationDB)
	accountRepo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	prefix := groupCategoryTestPrefix()

	g1 := mustCreateCategoryGroup(t, prefix+"g1")
	g2 := mustCreateCategoryGroup(t, prefix+"g2")
	g3 := mustCreateCategoryGroup(t, prefix+"g3")
	g4 := mustCreateCategoryGroup(t, prefix+"g4")
	created, err := catRepo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{Name: "Pick"}, {Name: "Other"}}, nil)
	require.NoError(t, err)
	pick, other := categoryByName(created)["Pick"].ID, categoryByName(created)["Other"].ID
	for _, groupID := range []int64{g1.ID, g3.ID, g4.ID} {
		_, err := catRepo.SetGroupCategories(ctx, groupID, []int64{pick})
		require.NoError(t, err)
	}
	_, err = catRepo.SetGroupCategories(ctx, g2.ID, []int64{other})
	require.NoError(t, err)

	inOne := mustCreateCategoryAccount(t, prefix+"a-one", g1.ID)
	mustCreateCategoryAccount(t, prefix+"a-outside", g2.ID)
	inBoth := mustCreateCategoryAccount(t, prefix+"a-both", g1.ID, g3.ID)
	mustCreateCategoryAccount(t, prefix+"a-deleted-group", g4.ID)
	mustCreateCategoryAccount(t, prefix+"a-ungrouped")
	softDeleteCategoryGroup(t, g4.ID)

	params := pagination.PaginationParams{Page: 1, PageSize: 50}
	groupCtx := service.WithGroupListCategoryFilter(ctx, pick)
	groupIDs := func(groups []service.Group) []int64 {
		out := make([]int64, 0, len(groups))
		for _, g := range groups {
			out = append(out, g.ID)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	groups, page, err := groupRepo.ListWithFilters(groupCtx, params, "", "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []int64{g1.ID, g3.ID}, groupIDs(groups))
	require.Equal(t, int64(2), page.Total)
	groups, page, err = groupRepo.ListWithFilters(groupCtx, pagination.PaginationParams{Page: 1, PageSize: 50, SortBy: "account_count", SortOrder: "desc"}, "", "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []int64{g1.ID, g3.ID}, groupIDs(groups))
	require.Equal(t, int64(2), page.Total)
	require.Equal(t, g1.ID, groups[0].ID, "g1 has two accounts, g3 one")
	groups, page, err = groupRepo.ListBindableWithFilters(groupCtx, params, "", "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []int64{g1.ID, g3.ID}, groupIDs(groups))
	require.Equal(t, int64(2), page.Total)
	groups, _, err = groupRepo.ListWithFilters(service.WithAccountGroupCategoryFilter(ctx, pick), params, "", "", prefix, nil)
	require.NoError(t, err)
	require.Equal(t, []int64{g1.ID, g2.ID, g3.ID}, groupIDs(groups), "the account key never narrows groups")
	groups, page, err = groupRepo.ListWithFilters(service.WithGroupListCategoryFilter(ctx, 99999999), params, "", "", "", nil)
	require.NoError(t, err)
	require.Empty(t, groups, "an unknown category gives an empty list")
	require.Zero(t, page.Total)

	accountIDs := func(accounts []service.Account) []int64 {
		out := make([]int64, 0, len(accounts))
		for _, a := range accounts {
			out = append(out, a.ID)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	accountCtx := service.WithAccountGroupCategoryFilter(ctx, pick)
	accounts, page, err := accountRepo.ListWithFilters(accountCtx, params, "", "", "", "", 0, "")
	require.NoError(t, err)
	require.Equal(t, []int64{inOne.ID, inBoth.ID}, accountIDs(accounts), "any live group counts, once per account")
	require.Equal(t, int64(2), page.Total)
	accounts, page, err = accountRepo.ListWithFilters(accountCtx, params, "", "", "", "", g3.ID, "")
	require.NoError(t, err)
	require.Equal(t, []int64{inBoth.ID}, accountIDs(accounts), "combined with the group filter")
	require.Equal(t, int64(1), page.Total)
	accounts, _, err = accountRepo.ListWithFilters(service.WithGroupListCategoryFilter(ctx, pick), params, "", "", "", prefix, 0, "")
	require.NoError(t, err)
	require.Len(t, accounts, 5, "the group list key never narrows accounts")

	all, err := accountRepo.ListAllWithFilters(accountCtx, "", "", "", prefix, 0, "")
	require.NoError(t, err)
	require.Len(t, all, 5, "the scheduler-score pool ignores the category filter")
}

func TestGroupCategoryWritesHaveNoSchedulerOrAuthSideEffects(t *testing.T) {
	resetGroupCategoryTables(t)
	ctx := context.Background()
	repo := NewGroupCategoryRepository(integrationDB)
	g := mustCreateCategoryGroup(t, groupCategoryTestPrefix()+"g")

	counts := func() (int64, int64) {
		var scheduler, auth int64
		require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox`).Scan(&scheduler))
		require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM auth_cache_invalidation_outbox`).Scan(&auth))
		return scheduler, auth
	}
	schedulerBefore, authBefore := counts()

	created, err := repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{Name: "S"}, {Name: "T"}}, nil)
	require.NoError(t, err)
	_, err = repo.SetGroupCategories(ctx, g.ID, []int64{created[0].ID, created[1].ID})
	require.NoError(t, err)
	_, err = repo.SaveGroupCategories(ctx, []service.GroupCategoryDraft{{ID: created[1].ID, Name: "T2"}}, []int64{created[0].ID})
	require.NoError(t, err)
	_, err = repo.SetGroupCategories(ctx, g.ID, nil)
	require.NoError(t, err)

	schedulerAfter, authAfter := counts()
	require.Equal(t, schedulerBefore, schedulerAfter, "category writes never touch the groups row")
	require.Equal(t, authBefore, authAfter)
}
