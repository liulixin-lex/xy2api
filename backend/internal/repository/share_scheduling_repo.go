package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type shareSchedulingRepository struct{ db *sql.DB }

// NewShareSchedulingRepository stores share-scheduling policies.
func NewShareSchedulingRepository(db *sql.DB) service.ShareSchedulingRepository {
	return &shareSchedulingRepository{db: db}
}

const shareSchedulingPolicyColumns = `group_id, version, scheduler, policy, COALESCE(imported_from, ''), updated_by, created_at, updated_at`

func scanShareSchedulingPolicy(row rowScanner) (*service.ShareSchedulingPolicyRow, error) {
	var out service.ShareSchedulingPolicyRow
	var updatedBy sql.NullInt64
	if err := row.Scan(&out.GroupID, &out.Version, &out.Scheduler, &out.Policy, &out.ImportedFrom, &updatedBy, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return nil, err
	}
	if updatedBy.Valid {
		id := updatedBy.Int64
		out.UpdatedBy = &id
	}
	return &out, nil
}

func (r *shareSchedulingRepository) ListPolicies(ctx context.Context) ([]service.ShareSchedulingPolicyRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+shareSchedulingPolicyColumns+` FROM share_scheduling_policies ORDER BY group_id`)
	if err != nil {
		return nil, fmt.Errorf("list share scheduling policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.ShareSchedulingPolicyRow
	for rows.Next() {
		row, err := scanShareSchedulingPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan share scheduling policy: %w", err)
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func (r *shareSchedulingRepository) GetPolicy(ctx context.Context, groupID int64) (*service.ShareSchedulingPolicyRow, error) {
	row, err := scanShareSchedulingPolicy(r.db.QueryRowContext(ctx, `SELECT `+shareSchedulingPolicyColumns+` FROM share_scheduling_policies WHERE group_id = $1`, groupID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get share scheduling policy: %w", err)
	}
	return row, nil
}

func (r *shareSchedulingRepository) SavePolicy(ctx context.Context, groupID, expectedVersion int64, scheduler string, policy []byte, updatedBy int64) (*service.ShareSchedulingPolicyRow, error) {
	var by sql.NullInt64
	if updatedBy > 0 {
		by = sql.NullInt64{Int64: updatedBy, Valid: true}
	}
	var row *service.ShareSchedulingPolicyRow
	var err error
	if expectedVersion <= 0 {
		row, err = scanShareSchedulingPolicy(r.db.QueryRowContext(ctx, `
			INSERT INTO share_scheduling_policies (group_id, version, scheduler, policy, updated_by)
			VALUES ($1, 1, $2, $3::jsonb, $4)
			ON CONFLICT (group_id) DO NOTHING
			RETURNING `+shareSchedulingPolicyColumns, groupID, scheduler, string(policy), by))
	} else {
		row, err = scanShareSchedulingPolicy(r.db.QueryRowContext(ctx, `
			UPDATE share_scheduling_policies
			SET version = version + 1, scheduler = $2, policy = $3::jsonb, updated_by = $4, updated_at = NOW()
			WHERE group_id = $1 AND version = $5
			RETURNING `+shareSchedulingPolicyColumns, groupID, scheduler, string(policy), by, expectedVersion))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrShareSchedulingVersionConflict
	}
	if err != nil {
		return nil, fmt.Errorf("save share scheduling policy: %w", err)
	}
	return row, nil
}

func (r *shareSchedulingRepository) AccountUsage(ctx context.Context, groupID int64, since time.Time) (map[int64]service.ShareSchedulingUsage, error) {
	groupFilter := `group_id = $1`
	args := []any{groupID, since}
	if groupID == 0 {
		// Group 0 governs ungrouped traffic, recorded without a group.
		groupFilter = `group_id IS NULL AND $1::bigint = 0`
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id, COUNT(*),
		       COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
		       COALESCE(SUM(total_cost), 0)
		FROM usage_logs
		WHERE `+groupFilter+` AND created_at >= $2
		GROUP BY account_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate share scheduling usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]service.ShareSchedulingUsage{}
	for rows.Next() {
		var accountID int64
		var usage service.ShareSchedulingUsage
		if err := rows.Scan(&accountID, &usage.Requests, &usage.Tokens, &usage.StandardCost); err != nil {
			return nil, fmt.Errorf("scan share scheduling usage: %w", err)
		}
		out[accountID] = usage
	}
	return out, rows.Err()
}
