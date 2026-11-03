package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.AccountRecentHealthBatchReader = (*usageLogRepository)(nil)

type recentHealthTxExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (r *usageLogRepository) GetAccountRecentHealthBatch(ctx context.Context, accountIDs []int64, now time.Time) (map[int64][]service.GroupQualityObservation, error) {
	result := make(map[int64][]service.GroupQualityObservation, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var tx recentHealthTxExecutor
	var rollback func()
	if r.db != nil {
		sqlTx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return nil, err
		}
		tx = sqlTx
		rollback = func() { _ = sqlTx.Rollback() }
	} else {
		// Integration suites pass an existing rollback transaction as the SQL executor.
		tx = r.sql
	}
	if rollback != nil {
		defer rollback()
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
		return nil, err
	}

	from := now.Add(-service.AccountRecentHistoryWindow).UTC()
	to := now.UTC()
	// Failed placeholders stay in LAG history but are not returned: one on another account breaks the
	// continuation; one on the same account keeps it, and its zero prompt keeps the next row out of cache samples.
	rows, err := tx.QueryContext(ctx, `
		WITH history AS MATERIALIZED (
			SELECT id, account_id, api_key_id, group_id, COALESCE(request_id, '') AS request_id,
			       created_at, stream, model,
			       (actual_cost > 0 OR output_tokens > 0) AS is_success,
			       COALESCE(NULLIF(end_to_end_first_output_ms, 0), first_token_ms, 0) AS first_output_ms,
			       COALESCE(session_id, '') AS session_id,
			       COALESCE(input_tokens, 0) + COALESCE(cache_read_tokens, 0) + COALESCE(cache_creation_tokens, 0) AS prompt_tokens,
			       COALESCE(cache_read_tokens, 0) AS cache_read_tokens,
			       LAG(account_id) OVER session_order AS previous_account_id,
			       LAG(model) OVER session_order AS previous_model,
			       LAG(created_at) OVER session_order AS previous_at,
			       LAG(COALESCE(input_tokens, 0) + COALESCE(cache_read_tokens, 0) + COALESCE(cache_creation_tokens, 0)) OVER session_order AS previous_prompt_tokens
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			WINDOW session_order AS (
				PARTITION BY api_key_id, COALESCE(origin_group_id, group_id), session_id
				ORDER BY created_at, id
			)
		)
		SELECT account_id, request_id, created_at, stream, first_output_ms, session_id,
		       prompt_tokens, cache_read_tokens,
		       COALESCE(session_id <> '' AND previous_account_id = account_id AND previous_model = model AND previous_at IS NOT NULL AND created_at - previous_at <= INTERVAL '5 minutes', FALSE),
		       COALESCE(previous_prompt_tokens, 0), TRUE
		FROM history
		WHERE account_id = ANY($3) AND is_success
		ORDER BY account_id, created_at, id`, from, to, pq.Array(accountIDs))
	if err != nil {
		return nil, fmt.Errorf("query account recent usage observations: %w", err)
	}
	for rows.Next() {
		var o service.GroupQualityObservation
		if err := rows.Scan(&o.AccountID, &o.RequestID, &o.At, &o.Streaming, &o.FirstOutputMs, &o.SessionID,
			&o.PromptTokens, &o.CacheReadTokens, &o.StrictContinuation, &o.PreviousPromptTokens, &o.Success); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan account recent usage observation: %w", err)
		}
		result[o.AccountID] = append(result[o.AccountID], o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("query account recent usage observations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	errors, err := loadGroupQualityErrorObservations(ctx, tx, accountIDs, now.Add(-service.AccountRecentFailureWindow), to)
	if err != nil {
		return nil, fmt.Errorf("query account recent soft failures: %w", err)
	}
	for _, observation := range errors {
		result[observation.AccountID] = append(result[observation.AccountID], observation)
	}
	return result, nil
}
