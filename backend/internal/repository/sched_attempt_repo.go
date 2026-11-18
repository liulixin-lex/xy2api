package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type schedAttemptRepository struct{ db *sql.DB }

var _ service.SchedAttemptStore = (*schedAttemptRepository)(nil)

func NewSchedAttemptRepository(db *sql.DB) service.SchedAttemptStore {
	return &schedAttemptRepository{db: db}
}

func (r *schedAttemptRepository) InsertBatch(ctx context.Context, rows []schedquality.AttemptSample) error {
	if len(rows) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, schedquality.LedgerWriteTimeout)
	defer cancel()
	var q strings.Builder
	_, _ = q.WriteString(`INSERT INTO account_attempt_samples
		(attempt_id, started_at, finished_at, account_id, group_id, model, request_class,
		stream, warm, ttfo_ms, censored_ms, outcome, detail, prompt_tokens, prev_prompt_tokens,
		cache_read_tokens, session_key, request_id, input_tokens, cache_creation_tokens,
		capture_version, first_event_ms, first_answer_ms, error_class, http_status, status_origin, output_bytes, full_tier_first_output_timeout) VALUES `)
	const columns = 28
	args := make([]any, 0, len(rows)*columns)
	for i, row := range rows {
		if i > 0 {
			_ = q.WriteByte(',')
		}
		_ = q.WriteByte('(')
		for j := 0; j < columns; j++ {
			if j > 0 {
				_ = q.WriteByte(',')
			}
			fmt.Fprintf(&q, "$%d", i*columns+j+1)
		}
		_ = q.WriteByte(')')
		args = append(args, row.AttemptID, row.StartedAt, row.FinishedAt, row.AccountID, row.GroupID,
			row.Model, 1, row.Stream, row.Warm, row.TTFOMs, row.CensoredMs, row.Outcome,
			row.Detail, row.PromptTokens, nil, row.CacheReadTokens, row.SessionKey, row.RequestID, row.InputTokens, row.CacheCreationTokens,
			schedquality.CurrentCaptureVersion, row.FirstEventMs, row.FirstAnswerMs, row.Class, row.HTTPStatus, row.StatusOrigin, row.OutputBytes, row.FullTierFirstOutputTimeout)
	}
	_, _ = q.WriteString(` ON CONFLICT (attempt_id) DO NOTHING`)
	_, err := r.db.ExecContext(ctx, q.String(), args...)
	return err
}

const schedAttemptColumns = `attempt_id, started_at, finished_at, account_id, group_id, model,
	stream, ttfo_ms, censored_ms, outcome, detail, prompt_tokens,
	cache_read_tokens, session_key, request_id, inserted_at, input_tokens, cache_creation_tokens,
	first_event_ms, first_answer_ms, error_class, http_status, status_origin, output_bytes,
	warm, full_tier_first_output_timeout, capture_version`

func (r *schedAttemptRepository) read(ctx context.Context, where string, args ...any) ([]schedquality.AttemptSample, error) {
	ctx, cancel := context.WithTimeout(ctx, schedquality.LedgerReadTimeout)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = '%dms'", schedquality.LedgerReadTimeout.Milliseconds())); err != nil {
		return nil, err
	}
	// Apply the evidence cutover centrally, including callers with their own WHERE.
	rows, err := tx.QueryContext(ctx, `SELECT `+schedAttemptColumns+` FROM
		(SELECT * FROM account_attempt_samples WHERE capture_version >= 2) AS account_attempt_samples `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []schedquality.AttemptSample
	for rows.Next() {
		var row schedquality.AttemptSample
		if err := rows.Scan(schedAttemptScanTargets(&row)...); err != nil {
			return nil, err
		}
		row.ClientCanceled = row.Outcome == schedquality.Outcome(5)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *schedAttemptRepository) LoadInsertedAfter(ctx context.Context, after time.Time, limit int) ([]schedquality.AttemptSample, time.Time, error) {
	// Legacy rows must not consume the page limit and stall the v2 cursor.
	rows, err := r.read(ctx, `WHERE inserted_at > $1 AND inserted_at <= (
		SELECT max(inserted_at) FROM (
			SELECT inserted_at FROM account_attempt_samples WHERE capture_version >= 2 AND inserted_at > $1
			ORDER BY inserted_at LIMIT NULLIF($2, 0)
		) AS page
	) ORDER BY inserted_at, attempt_id`, after, limit)
	var latest time.Time
	if len(rows) > 0 {
		latest = rows[len(rows)-1].InsertedAt
	}
	return rows, latest, err
}

func (r *schedAttemptRepository) DeleteBefore(ctx context.Context, t time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, schedquality.LedgerWriteTimeout)
	defer cancel()
	result, err := r.db.ExecContext(ctx, `DELETE FROM account_attempt_samples WHERE started_at < $1`, t)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
