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
