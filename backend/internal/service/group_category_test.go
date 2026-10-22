//go:build unit

package service

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type groupCategoryRepoFake struct {
	saveCalls   int
	savedItems  []GroupCategoryDraft
	savedDelete []int64
	setCalls    int
	setGroupID  int64
	setIDs      []int64
}

func (f *groupCategoryRepoFake) ListGroupCategories(context.Context) ([]GroupCategory, error) {
	return []GroupCategory{}, nil
}

func (f *groupCategoryRepoFake) SaveGroupCategories(_ context.Context, items []GroupCategoryDraft, deletedIDs []int64) ([]GroupCategory, error) {
	f.saveCalls++
	f.savedItems = append([]GroupCategoryDraft(nil), items...)
	f.savedDelete = append([]int64(nil), deletedIDs...)
	return []GroupCategory{}, nil
}

func (f *groupCategoryRepoFake) SetGroupCategories(_ context.Context, groupID int64, categoryIDs []int64) ([]int64, error) {
	f.setCalls++
	f.setGroupID = groupID
	f.setIDs = append([]int64(nil), categoryIDs...)
	return categoryIDs, nil
}

func TestNormalizeGroupCategoryName(t *testing.T) {
	name, err := NormalizeGroupCategoryName("　 公开分组 \t")
	require.NoError(t, err)
	require.Equal(t, "公开分组", name, "full-width and ASCII spaces are trimmed")

	name, err = NormalizeGroupCategoryName(strings.Repeat("分", 32))
	require.NoError(t, err)
	require.Equal(t, 32, len([]rune(name)))

	for _, raw := range []string{"", "   ", "　　", strings.Repeat("分", 33), "a\x00b", "line\nbreak", "tab\tinside", "del\x7f"} {
		_, err := NormalizeGroupCategoryName(raw)
		requireApplicationErrorReason(t, err, "GROUP_CATEGORY_NAME_INVALID")
	}
}

func TestValidateGroupCategoryNameSet(t *testing.T) {
	require.NoError(t, ValidateGroupCategoryNameSet(nil))
	require.NoError(t, ValidateGroupCategoryNameSet([]string{"公开分组", "专属分组", "Monitor"}))
	requireApplicationErrorReason(t, ValidateGroupCategoryNameSet([]string{"Monitor", "monitor"}), "GROUP_CATEGORY_NAME_DUPLICATE")

	names := make([]string, 0, MaxGroupCategories+1)
	for i := 0; i < MaxGroupCategories; i++ {
		names = append(names, "c"+strconv.Itoa(i))
	}
	require.NoError(t, ValidateGroupCategoryNameSet(names))
	requireApplicationErrorReason(t, ValidateGroupCategoryNameSet(append(names, "extra")), "GROUP_CATEGORY_LIMIT")
}

func TestNormalizeGroupCategoryIDs(t *testing.T) {
	ids, err := NormalizeGroupCategoryIDs([]int64{3, 1, 3, 2, 1})
	require.NoError(t, err)
	require.Equal(t, []int64{3, 1, 2}, ids, "duplicates are removed keeping the first position")

	ids, err = NormalizeGroupCategoryIDs(nil)
	require.NoError(t, err)
	require.NotNil(t, ids)
	require.Empty(t, ids)

	for _, bad := range [][]int64{{0}, {1, -2}} {
		_, err := NormalizeGroupCategoryIDs(bad)
		requireApplicationErrorReason(t, err, "GROUP_CATEGORY_IDS_INVALID")
	}
	tooMany := make([]int64, MaxGroupCategoryIDs+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	_, err = NormalizeGroupCategoryIDs(tooMany)
	requireApplicationErrorReason(t, err, "GROUP_CATEGORY_IDS_INVALID")
}

func TestParseGroupCategoryFilter(t *testing.T) {
	id, err := ParseGroupCategoryFilter("")
	require.NoError(t, err)
	require.Zero(t, id)

	id, err = ParseGroupCategoryFilter(" 3 ")
	require.NoError(t, err)
	require.Equal(t, int64(3), id)

	for _, raw := range []string{"0", "-1", "x", "3.5", "ungrouped"} {
		_, err := ParseGroupCategoryFilter(raw)
		requireApplicationErrorReason(t, err, "INVALID_GROUP_CATEGORY_FILTER")
	}
}

func TestGroupCategoryFilterContextKeysStaySeparate(t *testing.T) {
	ctx := context.Background()
	require.Zero(t, GroupListCategoryFilter(ctx))
	require.Zero(t, AccountGroupCategoryFilter(ctx))

	groupCtx := WithGroupListCategoryFilter(ctx, 4)
	require.Equal(t, int64(4), GroupListCategoryFilter(groupCtx))
	require.Zero(t, AccountGroupCategoryFilter(groupCtx), "the group list filter never narrows accounts")

	accountCtx := WithAccountGroupCategoryFilter(ctx, 7)
	require.Equal(t, int64(7), AccountGroupCategoryFilter(accountCtx))
	require.Zero(t, GroupListCategoryFilter(accountCtx), "the account list filter never narrows groups")
}

func TestGroupCategoryServiceSaveValidatesBeforeRepository(t *testing.T) {
	for _, tc := range []struct {
		name    string
		items   []GroupCategoryDraft
		deleted []int64
		reason  string
	}{
		{"blank name", []GroupCategoryDraft{{Name: "  "}}, nil, "GROUP_CATEGORY_NAME_INVALID"},
		{"duplicate name ignoring case", []GroupCategoryDraft{{ID: 1, Name: "Ops"}, {Name: " ops "}}, nil, "GROUP_CATEGORY_NAME_DUPLICATE"},
		{"duplicate item id", []GroupCategoryDraft{{ID: 1, Name: "a"}, {ID: 1, Name: "b"}}, nil, "GROUP_CATEGORY_IDS_INVALID"},
		{"negative item id", []GroupCategoryDraft{{ID: -1, Name: "a"}}, nil, "GROUP_CATEGORY_IDS_INVALID"},
		{"bad deleted id", []GroupCategoryDraft{{Name: "a"}}, []int64{0}, "GROUP_CATEGORY_IDS_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &groupCategoryRepoFake{}
			_, err := NewGroupCategoryService(repo).Save(context.Background(), tc.items, tc.deleted)
			requireApplicationErrorReason(t, err, tc.reason)
			require.Zero(t, repo.saveCalls)
		})
	}

	tooMany := make([]GroupCategoryDraft, 0, MaxGroupCategories+1)
	for i := 0; i <= MaxGroupCategories; i++ {
		tooMany = append(tooMany, GroupCategoryDraft{Name: "c" + strconv.Itoa(i)})
	}
	repo := &groupCategoryRepoFake{}
	_, err := NewGroupCategoryService(repo).Save(context.Background(), tooMany, nil)
	requireApplicationErrorReason(t, err, "GROUP_CATEGORY_LIMIT")
	require.Zero(t, repo.saveCalls)
}

func TestGroupCategoryServiceSaveDropsDeletedItemsAndTrimsNames(t *testing.T) {
	repo := &groupCategoryRepoFake{}
	_, err := NewGroupCategoryService(repo).Save(context.Background(), []GroupCategoryDraft{
		{ID: 4, Name: " 监控分组 "},
		{ID: 2, Name: "Ops"},
		{Name: "ops"}, // 与将被删除的 2 同名：删除后不算重名
		{Name: "　专属分组"},
	}, []int64{2, 2, 9})
	require.NoError(t, err)
	require.Equal(t, 1, repo.saveCalls)
	require.Equal(t, []GroupCategoryDraft{{ID: 4, Name: "监控分组"}, {Name: "ops"}, {Name: "专属分组"}}, repo.savedItems)
	require.Equal(t, []int64{2, 9}, repo.savedDelete)
}

func TestGroupCategoryServiceSetGroupCategories(t *testing.T) {
	repo := &groupCategoryRepoFake{}
	svc := NewGroupCategoryService(repo)

	ids, err := svc.SetGroupCategories(context.Background(), 7, []int64{3, 1, 3})
	require.NoError(t, err)
	require.Equal(t, []int64{3, 1}, ids)
	require.Equal(t, int64(7), repo.setGroupID)
	require.Equal(t, []int64{3, 1}, repo.setIDs)

	_, err = svc.SetGroupCategories(context.Background(), 7, []int64{0})
	requireApplicationErrorReason(t, err, "GROUP_CATEGORY_IDS_INVALID")
	_, err = svc.SetGroupCategories(context.Background(), 0, []int64{1})
	requireApplicationErrorReason(t, err, "GROUP_NOT_FOUND")
	require.Equal(t, 1, repo.setCalls)
}
