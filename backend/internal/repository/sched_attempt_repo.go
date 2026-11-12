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
		cache_read_tokens, session_key, request_id) VALUES `)
	args := make([]any, 0, len(rows)*18)
	for i, row := range rows {
		if i > 0 {
			_ = q.WriteByte(',')
		}
		_ = q.WriteByte('(')
		for j := 0; j < 18; j++ {
			if j > 0 {
				_ = q.WriteByte(',')
			}
			fmt.Fprintf(&q, "$%d", i*18+j+1)
		}
		_ = q.WriteByte(')')
		args = append(args, row.AttemptID, row.StartedAt, row.FinishedAt, row.AccountID, row.GroupID,
			row.Model, row.Class, row.Stream, row.Warm, row.TTFOMs, row.CensoredMs, row.Outcome,
			row.Detail, row.PromptTokens, row.PrevPromptTokens, row.CacheReadTokens, row.SessionKey, row.RequestID)
	}
	_, _ = q.WriteString(` ON CONFLICT (attempt_id) DO NOTHING`)
	_, err := r.db.ExecContext(ctx, q.String(), args...)
	return err
}

const schedAttemptColumns = `attempt_id, started_at, finished_at, account_id, group_id, model,
	request_class, stream, warm, ttfo_ms, censored_ms, outcome, detail, prompt_tokens,
	prev_prompt_tokens, cache_read_tokens, session_key, request_id, inserted_at`

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
	rows, err := tx.QueryContext(ctx, `SELECT `+schedAttemptColumns+` FROM account_attempt_samples `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []schedquality.AttemptSample
	for rows.Next() {
		var row schedquality.AttemptSample
		if err := rows.Scan(&row.AttemptID, &row.StartedAt, &row.FinishedAt, &row.AccountID, &row.GroupID,
			&row.Model, &row.Class, &row.Stream, &row.Warm, &row.TTFOMs, &row.CensoredMs, &row.Outcome,
			&row.Detail, &row.PromptTokens, &row.PrevPromptTokens, &row.CacheReadTokens, &row.SessionKey,
			&row.RequestID, &row.InsertedAt); err != nil {
			return nil, err
		}
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

func (r *schedAttemptRepository) LoadStartedSince(ctx context.Context, since time.Time) ([]schedquality.AttemptSample, error) {
	return r.read(ctx, `WHERE started_at > $1 ORDER BY started_at, attempt_id`, since)
}

func (r *schedAttemptRepository) LoadInsertedAfter(ctx context.Context, after time.Time, limit int) ([]schedquality.AttemptSample, time.Time, error) {
	rows, err := r.read(ctx, `WHERE inserted_at > $1 AND inserted_at <= (
		SELECT max(inserted_at) FROM (
			SELECT inserted_at FROM account_attempt_samples WHERE inserted_at > $1
			ORDER BY inserted_at LIMIT NULLIF($2, 0)
		) AS page
	) ORDER BY inserted_at, attempt_id`, after, limit)
	var latest time.Time
	if len(rows) > 0 {
		latest = rows[len(rows)-1].InsertedAt
	}
	return rows, latest, err
}

func (r *schedAttemptRepository) LoadRecentByAccount(ctx context.Context, accountID int64, limit int) ([]schedquality.AttemptSample, error) {
	return r.read(ctx, `WHERE account_id = $1 ORDER BY started_at DESC, attempt_id DESC LIMIT NULLIF($2, 0)`, accountID, limit)
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
