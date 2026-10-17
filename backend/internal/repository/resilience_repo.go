package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

// OutputTokenP99ByModel returns the P99 output tokens per requested model of
// successful usage since the given time (reservation output cap).
func (r *resilienceRepository) OutputTokenP99ByModel(ctx context.Context, since time.Time, minSamples int) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT LOWER(COALESCE(NULLIF(requested_model, ''), model)) AS m,
		       percentile_disc(0.99) WITHIN GROUP (ORDER BY output_tokens) AS p99
		FROM usage_logs
		WHERE created_at >= $1 AND output_tokens > 0
		GROUP BY 1
		HAVING COUNT(*) >= $2`, since, minSamples)
	if err != nil {
		return nil, fmt.Errorf("output token p99: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var model string
		var p99 int64
		if err := rows.Scan(&model, &p99); err != nil {
			return nil, fmt.Errorf("scan output token p99: %w", err)
		}
		out[model] = int(p99)
	}
	return out, rows.Err()
}

// CountBalanceStates returns the number of live accounts per resilience
// balance state (accounts without a record are OK).
func (r *resilienceRepository) CountBalanceStates(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(extra->'resilience_balance'->>'state', 'OK') AS state, COUNT(*)
		FROM accounts
		WHERE deleted_at IS NULL
		GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("count balance states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("scan balance state: %w", err)
		}
		out[state] = n
	}
	return out, rows.Err()
}
