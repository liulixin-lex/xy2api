package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type resilienceRepository struct {
	db *sql.DB
}

// NewResilienceRepository creates the SQL side of the resilience layer.
func NewResilienceRepository(db *sql.DB) service.ResilienceRepository {
	return &resilienceRepository{db: db}
}

// ListBalanceTrackedAccountIDs returns accounts that carry a non-OK balance
// record or a pause written by the balance state machine.
func (r *resilienceRepository) ListBalanceTrackedAccountIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM accounts
		WHERE deleted_at IS NULL
		  AND (
		    temp_unschedulable_reason LIKE 'balance:%'
		    OR COALESCE(extra->'resilience_balance'->>'state', 'OK') <> 'OK'
		  )
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list balance tracked accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan balance tracked account: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
