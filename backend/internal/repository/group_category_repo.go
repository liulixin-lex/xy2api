package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"

	dbent "github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/ent/group"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// 分组分类（迁移 278）。只写 group_categories / group_category_members，
// 不写 groups 行，所以不会触发调度重建和鉴权缓存失效。

type groupCategoryRepository struct{ db *sql.DB }

// NewGroupCategoryRepository stores the admin-only group categories.
func NewGroupCategoryRepository(db *sql.DB) service.GroupCategoryRepository {
	return &groupCategoryRepository{db: db}
}

// 已软删除的分组保留成员行，但读取时跳过。
const listGroupCategoriesSQL = `
SELECT c.id, c.name, c.sort_order,
       COALESCE(array_agg(g.id ORDER BY g.sort_order, g.id) FILTER (WHERE g.id IS NOT NULL), '{}')
FROM group_categories c
LEFT JOIN group_category_members m ON m.category_id = c.id
LEFT JOIN groups g ON g.id = m.group_id AND g.deleted_at IS NULL
GROUP BY c.id
ORDER BY c.sort_order, c.id`

func listGroupCategories(ctx context.Context, exec sqlExecutor) ([]service.GroupCategory, error) {
	rows, err := exec.QueryContext(ctx, listGroupCategoriesSQL)
	if err != nil {
		return nil, fmt.Errorf("list group categories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []service.GroupCategory{}
	for rows.Next() {
		var item service.GroupCategory
		var groupIDs []int64
		if err := rows.Scan(&item.ID, &item.Name, &item.SortOrder, pq.Array(&groupIDs)); err != nil {
			return nil, fmt.Errorf("scan group category: %w", err)
		}
		if groupIDs == nil {
			groupIDs = []int64{}
		}
		item.GroupIDs = groupIDs
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list group categories: %w", err)
	}
	return out, nil
}

func (r *groupCategoryRepository) ListGroupCategories(ctx context.Context) ([]service.GroupCategory, error) {
	return listGroupCategories(ctx, r.db)
}

func (r *groupCategoryRepository) SaveGroupCategories(ctx context.Context, items []service.GroupCategoryDraft, deletedIDs []int64) ([]service.GroupCategory, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin save group categories: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 表锁让所有保存串行执行，名称去重才可靠；外键检查拿的 KEY SHARE 行锁不受影响。
	if _, err := tx.ExecContext(ctx, `LOCK TABLE group_categories IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, fmt.Errorf("lock group categories: %w", err)
	}
	type existingCategory struct {
		name      string
		sortOrder int
	}
	existing := map[int64]existingCategory{}
	var existingOrder []int64
	rows, err := tx.QueryContext(ctx, `SELECT id, name, sort_order FROM group_categories ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("read group categories: %w", err)
	}
	for rows.Next() {
		var id int64
		var current existingCategory
		if err := rows.Scan(&id, &current.name, &current.sortOrder); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan group category: %w", err)
		}
		existing[id] = current
		existingOrder = append(existingOrder, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read group categories: %w", err)
	}
	_ = rows.Close()

	deleted := make(map[int64]struct{}, len(deletedIDs))
	toDelete := make([]int64, 0, len(deletedIDs))
	for _, id := range deletedIDs {
		deleted[id] = struct{}{}
		if _, ok := existing[id]; ok {
			toDelete = append(toDelete, id)
		}
	}
	listed := make(map[int64]struct{}, len(items))
	saved := make([]service.GroupCategoryDraft, 0, len(items))
	names := make([]string, 0, len(existing)+len(items))
	for _, item := range items {
		if item.ID > 0 {
			listed[item.ID] = struct{}{}
			// 已被别人删除的分类直接跳过。
			if _, ok := existing[item.ID]; !ok {
				continue
			}
			if _, ok := deleted[item.ID]; ok {
				continue
			}
		}
		saved = append(saved, item)
		names = append(names, item.Name)
	}
	// 两边都没列出的分类是别人同时新建的，保留并排在最后。
	unlisted := make([]int64, 0)
	for _, id := range existingOrder {
		_, isDeleted := deleted[id]
		_, isListed := listed[id]
		if isDeleted || isListed {
			continue
		}
		unlisted = append(unlisted, id)
		names = append(names, existing[id].name)
	}
	if err := service.ValidateGroupCategoryNameSet(names); err != nil {
		return nil, err
	}

	if len(toDelete) > 0 {
		// 按 id 顺序加行锁，和分组分类保存的 FOR KEY SHARE 顺序一致，避免死锁。
		sort.Slice(toDelete, func(i, j int) bool { return toDelete[i] < toDelete[j] })
		if _, err := queryInt64s(ctx, tx, `SELECT id FROM group_categories WHERE id = ANY($1) ORDER BY id FOR UPDATE`, pq.Array(toDelete)); err != nil {
			return nil, fmt.Errorf("lock deleted group categories: %w", err)
		}
		// 成员行由外键级联删除。
		if _, err := tx.ExecContext(ctx, `DELETE FROM group_categories WHERE id = ANY($1)`, pq.Array(toDelete)); err != nil {
			return nil, fmt.Errorf("delete group categories: %w", err)
		}
	}
	position := 0
	for _, item := range saved {
		if item.ID > 0 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE group_categories SET name = $2, sort_order = $3, updated_at = NOW()
				WHERE id = $1 AND (name <> $2 OR sort_order <> $3)`, item.ID, item.Name, position); err != nil {
				return nil, fmt.Errorf("update group category: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO group_categories (name, sort_order) VALUES ($1, $2)`, item.Name, position); err != nil {
			return nil, fmt.Errorf("create group category: %w", err)
		}
		position++
	}
	for _, id := range unlisted {
		if existing[id].sortOrder != position {
			if _, err := tx.ExecContext(ctx, `UPDATE group_categories SET sort_order = $2, updated_at = NOW() WHERE id = $1`, id, position); err != nil {
				return nil, fmt.Errorf("reorder group category: %w", err)
			}
		}
		position++
	}

	out, err := listGroupCategories(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit group categories: %w", err)
	}
	return out, nil
}

func (r *groupCategoryRepository) SetGroupCategories(ctx context.Context, groupID int64, categoryIDs []int64) ([]int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin set group categories: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// FOR NO KEY UPDATE 让同一分组的两次保存串行，不阻塞网关读取和其他表的外键检查。
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM groups WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, groupID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock group: %w", err)
	}
	// 不存在或刚被删除的分类直接丢掉；KEY SHARE 锁让并发的分类删除等本事务提交后再级联。
	live, err := queryInt64s(ctx, tx, `SELECT id FROM group_categories WHERE id = ANY($1) ORDER BY id FOR KEY SHARE`, pq.Array(categoryIDs))
	if err != nil {
		return nil, fmt.Errorf("read group categories: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_category_members WHERE group_id = $1 AND NOT (category_id = ANY($2))`, groupID, pq.Array(live)); err != nil {
		return nil, fmt.Errorf("delete group category members: %w", err)
	}
	if len(live) > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO group_category_members (category_id, group_id)
			SELECT unnest($1::bigint[]), $2
			ON CONFLICT DO NOTHING`, pq.Array(live), groupID); err != nil {
			return nil, fmt.Errorf("insert group category members: %w", err)
		}
	}
	out, err := queryInt64s(ctx, tx, `SELECT id FROM group_categories WHERE id = ANY($1) ORDER BY sort_order, id`, pq.Array(live))
	if err != nil {
		return nil, fmt.Errorf("read group categories: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit group categories: %w", err)
	}
	return out, nil
}

func queryInt64s(ctx context.Context, exec sqlExecutor, query string, args ...any) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// applyGroupListCategoryFilter 分组列表按 ?group_category 筛选（经请求上下文传入）。
func applyGroupListCategoryFilter(ctx context.Context, q *dbent.GroupQuery) *dbent.GroupQuery {
	categoryID := service.GroupListCategoryFilter(ctx)
	if categoryID <= 0 {
		return q
	}
	return q.Where(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString("EXISTS (SELECT 1 FROM group_category_members gcm WHERE gcm.group_id = ").
				Ident(s.C(group.FieldID)).
				WriteString(" AND gcm.category_id = ").
				Arg(categoryID).
				WriteString(")")
		}))
	})
}

// applyAccountGroupCategoryFilter 账号列表按 ?group_category 筛选：账号绑定的任一未删除分组
// （含停用分组）属于该分类即命中。调度评分池不调用它，和 IQ 状态筛选一样。
func applyAccountGroupCategoryFilter(ctx context.Context, q *dbent.AccountQuery) *dbent.AccountQuery {
	categoryID := service.AccountGroupCategoryFilter(ctx)
	if categoryID <= 0 {
		return q
	}
	return q.Where(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString("EXISTS (SELECT 1 FROM account_groups ag" +
				" JOIN group_category_members gcm ON gcm.group_id = ag.group_id" +
				" JOIN groups g ON g.id = ag.group_id AND g.deleted_at IS NULL" +
				" WHERE ag.account_id = ").
				Ident(s.C(dbaccount.FieldID)).
				WriteString(" AND gcm.category_id = ").
				Arg(categoryID).
				WriteString(")")
		}))
	})
}
