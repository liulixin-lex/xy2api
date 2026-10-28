package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type operatorAccessQueryRepository struct{ db *sql.DB }

func NewOperatorAccessQueryRepository(db *sql.DB) service.OperatorAccessQueryRepository {
	return &operatorAccessQueryRepository{db: db}
}
func (r *operatorAccessQueryRepository) GroupHasProtectedMembers(ctx context.Context, groupID, operatorID int64) (bool, error) {
	var found bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_subscriptions us JOIN users u ON u.id = us.user_id WHERE us.group_id = $1 AND us.deleted_at IS NULL AND (u.role <> 'user' OR u.id = $2) UNION ALL SELECT 1 FROM user_allowed_groups uag JOIN users u ON u.id = uag.user_id WHERE uag.group_id = $1 AND (u.role <> 'user' OR u.id = $2))`, groupID, operatorID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("query protected group members: %w", err)
	}
	return found, nil
}
func (r *operatorAccessQueryRepository) ProxyIDsUsedByAccounts(ctx context.Context, proxyIDs []int64) ([]int64, error) {
	ids := []int64{}
	if len(proxyIDs) == 0 {
		return ids, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT p FROM (SELECT proxy_id AS p FROM accounts WHERE deleted_at IS NULL AND proxy_id = ANY($1) UNION SELECT proxy_fallback_origin_id FROM accounts WHERE deleted_at IS NULL AND proxy_fallback_origin_id = ANY($1)) t`, pq.Array(proxyIDs))
	if err != nil {
		return nil, fmt.Errorf("query account proxies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan account proxy: %w", err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read account proxies: %w", err)
	}
	return ids, nil
}
