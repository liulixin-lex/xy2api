package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lib/pq"

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

// shareSchedulingStatementTimeout bounds every admin read of the log tables
// (usage_logs, ops_error_logs) on the database side.
const shareSchedulingStatementTimeout = "5s"

// readLogs runs fn in a read-only transaction whose statements time out
// after shareSchedulingStatementTimeout.
func (r *shareSchedulingRepository) readLogs(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '`+shareSchedulingStatementTimeout+`'`); err != nil {
		return err
	}
	return fn(tx)
}

// shareGroupFilter selects the rows of a group; group 0 governs ungrouped
// traffic, recorded without a group.
func shareGroupFilter(groupID int64) string {
	if groupID == 0 {
		return `group_id IS NULL AND $1::bigint = 0`
	}
	return `group_id = $1`
}

func (r *shareSchedulingRepository) AccountUsage(ctx context.Context, groupID int64, from, to time.Time) (map[int64]service.ShareSchedulingUsage, error) {
	out := map[int64]service.ShareSchedulingUsage{}
	err := r.readLogs(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT account_id, COUNT(*),
			       COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
			       COALESCE(SUM(total_cost), 0)
			FROM usage_logs
			WHERE `+shareGroupFilter(groupID)+` AND created_at >= $2 AND created_at < $3
			GROUP BY account_id`, groupID, from, to)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var accountID int64
			var usage service.ShareSchedulingUsage
			if err := rows.Scan(&accountID, &usage.Requests, &usage.Tokens, &usage.StandardCost); err != nil {
				return err
			}
			out[accountID] = usage
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate share scheduling usage: %w", err)
	}
	return out, nil
}

func (r *shareSchedulingRepository) GroupSummaries(ctx context.Context, since time.Time) (map[int64]service.ShareSchedulingGroupSummary, error) {
	out := map[int64]service.ShareSchedulingGroupSummary{}
	err := r.readLogs(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT group_id, COUNT(*), COALESCE(SUM(cache_read_tokens), 0),
			       COALESCE(SUM(input_tokens + cache_read_tokens + cache_creation_tokens), 0)
			FROM usage_logs
			WHERE group_id IS NOT NULL AND created_at >= $1
			GROUP BY group_id`, since)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var groupID int64
			var summary service.ShareSchedulingGroupSummary
			if err := rows.Scan(&groupID, &summary.Requests, &summary.CacheReadTokens, &summary.PromptTokens); err != nil {
				return err
			}
			out[groupID] = summary
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("summarise share scheduling groups: %w", err)
	}
	return out, nil
}

// GroupMetrics reads a group's successful requests with their cache and
// first-token figures from usage_logs, and its client-visible errors from
// ops_error_logs: status 400 or more, or a cyber policy block, which is the
// condition of the account recent health (recovered upstream rows are not
// errors).
func (r *shareSchedulingRepository) GroupMetrics(ctx context.Context, groupID int64, from, to time.Time) (service.ShareSchedulingGroupMetrics, error) {
	var out service.ShareSchedulingGroupMetrics
	err := r.readLogs(ctx, func(tx *sql.Tx) error {
		var p50, p95 sql.NullFloat64
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(cache_read_tokens), 0),
			       COALESCE(SUM(input_tokens + cache_read_tokens + cache_creation_tokens), 0),
			       COUNT(first_token_ms),
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms),
			       percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms)
			FROM usage_logs
			WHERE `+shareGroupFilter(groupID)+` AND created_at >= $2 AND created_at < $3`, groupID, from, to).
			Scan(&out.Requests, &out.CacheReadTokens, &out.PromptTokens, &out.TTFTSamples, &p50, &p95); err != nil {
			return err
		}
		out.TTFTP50Ms, out.TTFTP95Ms = shareRoundedMs(p50), shareRoundedMs(p95)
		return tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM ops_error_logs
			WHERE `+shareGroupFilter(groupID)+` AND created_at >= $2 AND created_at < $3
			  AND (COALESCE(status_code, 0) >= 400 OR error_type = 'cyber_policy')`, groupID, from, to).
			Scan(&out.Errors)
	})
	if err != nil {
		return out, fmt.Errorf("read share scheduling group metrics: %w", err)
	}
	return out, nil
}

func shareRoundedMs(v sql.NullFloat64) *int {
	if !v.Valid {
		return nil
	}
	ms := int(v.Float64 + 0.5)
	return &ms
}

// shareSchedulingMaxAttemptGroups bounds the failed-attempt rows one health
// read returns.
const shareSchedulingMaxAttemptGroups = 50000

// AccountAttempts reads the successful requests of the accounts from
// usage_logs and their failed upstream attempts from
// ops_error_logs.upstream_errors, grouped by what the health factor tells
// apart (design §11.1).
func (r *shareSchedulingRepository) AccountAttempts(ctx context.Context, accountIDs []int64, from, to time.Time) (service.ShareSchedulingAccountAttempts, error) {
	out := service.ShareSchedulingAccountAttempts{Successes: map[int64]int64{}}
	if len(accountIDs) == 0 {
		return out, nil
	}
	ids := pq.Array(accountIDs)
	err := r.readLogs(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT account_id, COUNT(*)
			FROM usage_logs
			WHERE account_id = ANY($1) AND created_at >= $2 AND created_at < $3
			GROUP BY account_id`, ids, from, to)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, n int64
			if err := rows.Scan(&id, &n); err != nil {
				_ = rows.Close()
				return err
			}
			out.Successes[id] = n
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `
			SELECT (ev->>'account_id')::bigint,
			       COALESCE(ev->>'kind', ''),
			       CASE WHEN jsonb_typeof(ev->'upstream_status_code') = 'number' THEN (ev->>'upstream_status_code')::int ELSE 0 END,
			       COALESCE(ev->>'resilience_category', ''),
			       LEFT(COALESCE(ev->>'message', ''), 200),
			       COUNT(*)
			FROM ops_error_logs e
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(e.upstream_errors) = 'array' THEN e.upstream_errors ELSE '[]'::jsonb END) AS ev
			WHERE e.created_at >= $2 AND e.created_at < $3 AND e.upstream_errors IS NOT NULL
			  AND jsonb_typeof(ev->'account_id') = 'number'
			  AND (ev->>'account_id')::bigint = ANY($1)
			GROUP BY 1, 2, 3, 4, 5
			LIMIT `+strconv.Itoa(shareSchedulingMaxAttemptGroups), ids, from, to)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a service.ShareSchedulingAttemptCount
			if err := rows.Scan(&a.AccountID, &a.Kind, &a.StatusCode, &a.Category, &a.Message, &a.Count); err != nil {
				return err
			}
			out.Failures = append(out.Failures, a)
		}
		return rows.Err()
	})
	if err != nil {
		return service.ShareSchedulingAccountAttempts{}, fmt.Errorf("read share scheduling account attempts: %w", err)
	}
	return out, nil
}
