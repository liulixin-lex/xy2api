package repository

import (
	"context"
	"fmt"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.UserStatusBatchRepository = (*userRepository)(nil)

// BatchUpdateStatus 见 service.UserStatusBatchRepository。整批只发一条 UPDATE，RETURNING
// 只带回状态真正变化的行，供上层按用户失效认证缓存。
func (r *userRepository) BatchUpdateStatus(ctx context.Context, userIDs []int64, status string) ([]int64, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	args := []any{status, pq.Array(userIDs)}
	query := "UPDATE users SET status = $1, updated_at = NOW() WHERE id = ANY($2) AND deleted_at IS NULL AND status <> $1"
	if status == service.StatusDisabled {
		args = append(args, service.RoleAdmin)
		query += " AND role <> $3"
	}
	query += " RETURNING id"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch update user status: %w", err)
	}
	defer func() { _ = rows.Close() }()

	updated := make([]int64, 0, len(userIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan batch user status result: %w", err)
		}
		updated = append(updated, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("batch update user status: %w", err)
	}
	return updated, nil
}
