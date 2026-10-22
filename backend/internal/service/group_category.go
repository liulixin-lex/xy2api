package service

import (
	"context"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// 分组分类：只用于后台整理分组的标签，存在独立的表里（迁移 278）。
// 网关、调度、计费、用户端接口和 API Key 鉴权快照都不读分类；保存分类也不写 groups 行。

const (
	MaxGroupCategories        = 50
	MaxGroupCategoryNameRunes = 32
	MaxGroupCategoryIDs       = 50
)

var (
	ErrGroupCategoryNameInvalid   = infraerrors.BadRequest("GROUP_CATEGORY_NAME_INVALID", "group category name must have 1-32 characters and no control characters")
	ErrGroupCategoryNameDuplicate = infraerrors.Conflict("GROUP_CATEGORY_NAME_DUPLICATE", "group category name already exists")
	ErrGroupCategoryLimit         = infraerrors.BadRequest("GROUP_CATEGORY_LIMIT", "at most 50 group categories")
	ErrGroupCategoryIDsInvalid    = infraerrors.BadRequest("GROUP_CATEGORY_IDS_INVALID", "invalid group category ids")
	ErrGroupCategoryFilterInvalid = infraerrors.BadRequest("INVALID_GROUP_CATEGORY_FILTER", "invalid group_category filter")
)

// GroupCategory 一个分类及其未删除的分组（按分组排序）。
type GroupCategory struct {
	ID        int64
	Name      string
	SortOrder int
	GroupIDs  []int64
}

// GroupCategoryDraft 保存分类列表时的一项；ID 为 0 表示新建。
type GroupCategoryDraft struct {
	ID   int64
	Name string
}

type GroupCategoryRepository interface {
	ListGroupCategories(ctx context.Context) ([]GroupCategory, error)
	// SaveGroupCategories 按 items 顺序改名、新建并排序，只删除 deletedIDs；
	// 两边都没列出的分类（别人同时新建的）保留并排在最后。
	SaveGroupCategories(ctx context.Context, items []GroupCategoryDraft, deletedIDs []int64) ([]GroupCategory, error)
	// SetGroupCategories 整体替换一个分组的分类，丢弃不存在的分类 id，按分类顺序返回。
	SetGroupCategories(ctx context.Context, groupID int64, categoryIDs []int64) ([]int64, error)
}

// NormalizeGroupCategoryName 去掉首尾空白（含全角空格），拒绝空名、超过 32 字和控制字符。
func NormalizeGroupCategoryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxGroupCategoryNameRunes {
		return "", ErrGroupCategoryNameInvalid
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrGroupCategoryNameInvalid
		}
	}
	return name, nil
}

// ValidateGroupCategoryNameSet 检查分类总数上限和名称去重（不区分大小写）。
func ValidateGroupCategoryNameSet(names []string) error {
	if len(names) > MaxGroupCategories {
		return ErrGroupCategoryLimit
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return ErrGroupCategoryNameDuplicate
		}
		seen[key] = struct{}{}
	}
	return nil
}

// NormalizeGroupCategoryIDs 拒绝非正数和超过 50 个的 id，去重并保持首次出现的顺序。
func NormalizeGroupCategoryIDs(ids []int64) ([]int64, error) {
	if len(ids) > MaxGroupCategoryIDs {
		return nil, ErrGroupCategoryIDsInvalid
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrGroupCategoryIDsInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// ParseGroupCategoryFilter 解析 ?group_category：空串表示不筛选（0），否则必须是正整数。
func ParseGroupCategoryFilter(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrGroupCategoryFilterInvalid
	}
	return id, nil
}

// 分组列表和账号列表各用一个 context key，账号列表的筛选不会误筛分组列表。
type groupListCategoryFilterKey struct{}
type accountGroupCategoryFilterKey struct{}

func WithGroupListCategoryFilter(ctx context.Context, categoryID int64) context.Context {
	return context.WithValue(ctx, groupListCategoryFilterKey{}, categoryID)
}
func GroupListCategoryFilter(ctx context.Context) int64 {
	value, _ := ctx.Value(groupListCategoryFilterKey{}).(int64)
	return value
}

func WithAccountGroupCategoryFilter(ctx context.Context, categoryID int64) context.Context {
	return context.WithValue(ctx, accountGroupCategoryFilterKey{}, categoryID)
}
func AccountGroupCategoryFilter(ctx context.Context) int64 {
	value, _ := ctx.Value(accountGroupCategoryFilterKey{}).(int64)
	return value
}

type GroupCategoryService struct {
	repo GroupCategoryRepository
}

func NewGroupCategoryService(repo GroupCategoryRepository) *GroupCategoryService {
	return &GroupCategoryService{repo: repo}
}

func (s *GroupCategoryService) List(ctx context.Context) ([]GroupCategory, error) {
	return s.repo.ListGroupCategories(ctx)
}

// Save 校验后整体保存分类列表；不合法的输入不会到达仓储。
func (s *GroupCategoryService) Save(ctx context.Context, items []GroupCategoryDraft, deletedIDs []int64) ([]GroupCategory, error) {
	drafts := make([]GroupCategoryDraft, 0, len(items))
	itemIDs := make(map[int64]struct{}, len(items))
	for _, item := range items {
		name, err := NormalizeGroupCategoryName(item.Name)
		if err != nil {
			return nil, err
		}
		if item.ID < 0 {
			return nil, ErrGroupCategoryIDsInvalid
		}
		if item.ID > 0 {
			if _, ok := itemIDs[item.ID]; ok {
				return nil, ErrGroupCategoryIDsInvalid
			}
			itemIDs[item.ID] = struct{}{}
		}
		drafts = append(drafts, GroupCategoryDraft{ID: item.ID, Name: name})
	}
	deleted, err := NormalizeGroupCategoryIDs(deletedIDs)
	if err != nil {
		return nil, err
	}
	deletedSet := make(map[int64]struct{}, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = struct{}{}
	}
	kept := drafts[:0]
	names := make([]string, 0, len(drafts))
	for _, draft := range drafts {
		if _, ok := deletedSet[draft.ID]; ok && draft.ID > 0 {
			continue
		}
		kept = append(kept, draft)
		names = append(names, draft.Name)
	}
	if err := ValidateGroupCategoryNameSet(names); err != nil {
		return nil, err
	}
	return s.repo.SaveGroupCategories(ctx, kept, deleted)
}

func (s *GroupCategoryService) SetGroupCategories(ctx context.Context, groupID int64, categoryIDs []int64) ([]int64, error) {
	if groupID <= 0 {
		return nil, ErrGroupNotFound
	}
	ids, err := NormalizeGroupCategoryIDs(categoryIDs)
	if err != nil {
		return nil, err
	}
	return s.repo.SetGroupCategories(ctx, groupID, ids)
}
