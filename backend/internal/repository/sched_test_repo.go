package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type schedTestRepository struct{ db *sql.DB }

func NewSchedTestRepository(db *sql.DB) service.SchedTestStore { return &schedTestRepository{db: db} }

func (r *schedTestRepository) Insert(ctx context.Context, v service.SchedTestRecord) error {
	status := 2
	if v.Running {
		status = 0
	} else if v.Done {
		status = 1
	}
	trigger := 1
	if v.Auto {
		trigger = 0
	}
	var finished any
	if !v.FinishedAt.IsZero() {
		finished = v.FinishedAt
	}
	turns, err := json.Marshal(v.Turns)
	if err != nil {
		return err
	}
	if v.Turns == nil {
		turns = []byte("[]")
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO account_quality_tests (id,account_id,model,trigger,operator_id,status,abort_reason,started_at,finished_at,turns,input_tokens,cached_tokens,output_tokens) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, v.ID, v.AccountID, v.Model, trigger, v.OperatorID, status, v.AbortReason, v.StartedAt, finished, string(turns), v.InputTokens, v.CachedTokens, v.OutputTokens)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "uq_quality_tests_running" {
		return service.ErrSchedTestRunning
	}
	return err
}
func (r *schedTestRepository) Finish(ctx context.Context, v service.SchedTestRecord) error {
	turns, err := json.Marshal(v.Turns)
	if err != nil {
		return err
	}
	if v.Turns == nil {
		turns = []byte("[]")
	}
	status := 2
	if v.Done {
		status = 1
	}
	_, err = r.db.ExecContext(ctx, `UPDATE account_quality_tests SET status=$2,abort_reason=$3,finished_at=$4,turns=$5,input_tokens=$6,cached_tokens=$7,output_tokens=$8 WHERE id=$1 AND status=0`, v.ID, status, v.AbortReason, v.FinishedAt, string(turns), v.InputTokens, v.CachedTokens, v.OutputTokens)
	return err
}

const schedTestColumns = `id,account_id,model,trigger,operator_id,status,abort_reason,started_at,finished_at,turns,input_tokens,cached_tokens,output_tokens`

type schedTestScanner interface{ Scan(...any) error }

func scanSchedTest(row schedTestScanner) (service.SchedTestRecord, error) {
	var v service.SchedTestRecord
	var trigger, status int
	var finished sql.NullTime
	var turns []byte
	err := row.Scan(&v.ID, &v.AccountID, &v.Model, &trigger, &v.OperatorID, &status, &v.AbortReason, &v.StartedAt, &finished, &turns, &v.InputTokens, &v.CachedTokens, &v.OutputTokens)
	if err != nil {
		return v, err
	}
	v.Auto = trigger == 0
	v.Running = status == 0
	v.Done = status == 1
	if finished.Valid {
		v.FinishedAt = finished.Time
	}
	err = json.Unmarshal(turns, &v.Turns)
	return v, err
}
func (r *schedTestRepository) Latest(ctx context.Context, id int64, model string) (*service.SchedTestRecord, error) {
	v, err := scanSchedTest(r.db.QueryRowContext(ctx, `SELECT `+schedTestColumns+` FROM account_quality_tests WHERE account_id=$1 AND model=$2 ORDER BY started_at DESC LIMIT 1`, id, model))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
func (r *schedTestRepository) query(ctx context.Context, q string, args ...any) ([]service.SchedTestRecord, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []service.SchedTestRecord
	for rows.Next() {
		v, err := scanSchedTest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (r *schedTestRepository) Since(ctx context.Context, since time.Time) ([]service.SchedTestRecord, error) {
	return r.query(ctx, `SELECT `+schedTestColumns+` FROM account_quality_tests WHERE started_at >= $1 ORDER BY started_at ASC,id ASC`, since)
}
func (r *schedTestRepository) Recent(ctx context.Context, id int64, limit int) ([]service.SchedTestRecord, error) {
	return r.query(ctx, `SELECT `+schedTestColumns+` FROM account_quality_tests WHERE account_id=$1 ORDER BY started_at DESC,id DESC LIMIT $2`, id, limit)
}
func (r *schedTestRepository) ReapStale(ctx context.Context, before, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE account_quality_tests SET status=2,abort_reason='timeout',finished_at=$2 WHERE status=0 AND started_at < $1`, before, now)
	return err
}
func (r *schedTestRepository) DeleteBefore(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM account_quality_tests WHERE started_at < $1 AND status<>0`, before)
	return err
}
