package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type openAITOTPRepository struct{ db *sql.DB }
type openAITOTPStore struct{ tx *sql.Tx }

func NewOpenAITOTPRepository(db *sql.DB) service.OpenAITOTPRepository {
	return &openAITOTPRepository{db: db}
}
func (r *openAITOTPRepository) WithTx(ctx context.Context, fn func(service.OpenAITOTPStore) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// This namespace is separate from accounts and user TOTP. One distributed
	// transaction lock keeps CAS, queue/recovery and import consistent on replicas.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(741285037442)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL synchronous_commit = on`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_totp_tasks SET
  state = CASE WHEN operation = 'totp_rotation' AND
   (action <> 'rotate' OR state IN ('enrolling','prepared','activating','verifying')) THEN 'uncertain' ELSE 'failed' END,
  payload = CASE WHEN operation = 'totp_rotation' AND
   (action <> 'rotate' OR state IN ('enrolling','prepared','activating','verifying')) THEN payload ELSE '' END,
  error_code = 'worker_timeout', claim_hash = '', worker_id = '', lease_until = NULL, updated_at = NOW()
  WHERE state IN ('running','enrolling','prepared','activating','verifying')
   AND (lease_until <= NOW() OR started_at + INTERVAL '15 minutes' <= NOW())`); err != nil {
		return err
	}
	if err = fn(&openAITOTPStore{tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *openAITOTPStore) Credential(ctx context.Context, id int64) (*service.OpenAITOTPCredentialRecord, error) {
	c := &service.OpenAITOTPCredentialRecord{AccountID: id}
	err := s.tx.QueryRowContext(ctx, `SELECT revision,email,payload FROM openai_totp_credentials WHERE account_id=$1`, id).Scan(&c.Revision, &c.Email, &c.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}
func (s *openAITOTPStore) SaveCredential(ctx context.Context, c *service.OpenAITOTPCredentialRecord) error {
	_, err := s.tx.ExecContext(ctx, `INSERT INTO openai_totp_credentials(account_id,revision,email,payload) VALUES($1,$2,$3,$4)
 ON CONFLICT(account_id) DO UPDATE SET revision=EXCLUDED.revision,email=EXCLUDED.email,payload=EXCLUDED.payload,updated_at=NOW()`, c.AccountID, c.Revision, c.Email, c.Payload)
	return err
}

const openAITOTPTaskColumns = `id,COALESCE(account_id,0),state,action,error_code,has_candidate,operation,payload,claim_hash,worker_id,lease_until,started_at`

func scanOpenAITOTPTask(row *sql.Row) (*service.OpenAITOTPTaskRecord, error) {
	t := &service.OpenAITOTPTaskRecord{}
	var lease, started sql.NullTime
	err := row.Scan(&t.TaskID, &t.AccountID, &t.State, &t.Action, &t.ErrorCode, &t.HasCandidate, &t.Operation, &t.Payload, &t.ClaimHash, &t.WorkerID, &lease, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.LeaseUntil = lease.Time
	t.StartedAt = started.Time
	return t, nil
}
func (s *openAITOTPStore) Task(ctx context.Context, id int64) (*service.OpenAITOTPTaskRecord, error) {
	return scanOpenAITOTPTask(s.tx.QueryRowContext(ctx, `SELECT `+openAITOTPTaskColumns+` FROM openai_totp_tasks WHERE id=$1`, id))
}
func (s *openAITOTPStore) ActiveTask(ctx context.Context, id int64) (*service.OpenAITOTPTaskRecord, error) {
	return scanOpenAITOTPTask(s.tx.QueryRowContext(ctx, `SELECT `+openAITOTPTaskColumns+` FROM openai_totp_tasks
 WHERE account_id=$1 AND operation='totp_rotation' AND state IN ('queued','running','enrolling','prepared','activating','verifying','uncertain') ORDER BY id DESC LIMIT 1`, id))
}
func (s *openAITOTPStore) LatestTask(ctx context.Context, id int64) (*service.OpenAITOTPTaskRecord, error) {
	return scanOpenAITOTPTask(s.tx.QueryRowContext(ctx, `SELECT `+openAITOTPTaskColumns+` FROM openai_totp_tasks WHERE account_id=$1 AND operation='totp_rotation' ORDER BY id DESC LIMIT 1`, id))
}
func (s *openAITOTPStore) NextTask(ctx context.Context) (*service.OpenAITOTPTaskRecord, error) {
	return scanOpenAITOTPTask(s.tx.QueryRowContext(ctx, `SELECT `+openAITOTPTaskColumns+` FROM openai_totp_tasks WHERE state='queued' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`))
}
func (s *openAITOTPStore) SaveTask(ctx context.Context, t *service.OpenAITOTPTaskRecord) error {
	var accountID, lease, started any
	if t.AccountID > 0 {
		accountID = t.AccountID
	}
	if !t.LeaseUntil.IsZero() {
		lease = t.LeaseUntil
	}
	if !t.StartedAt.IsZero() {
		started = t.StartedAt
	}
	args := []any{accountID, t.State, t.Action, t.ErrorCode, t.HasCandidate, t.Operation, t.Payload, t.ClaimHash, t.WorkerID, lease, started}
	if t.TaskID == 0 {
		return s.tx.QueryRowContext(ctx, `INSERT INTO openai_totp_tasks(account_id,state,action,error_code,has_candidate,operation,payload,claim_hash,worker_id,lease_until,started_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, args...).Scan(&t.TaskID)
	}
	args = append(args, t.TaskID)
	_, err := s.tx.ExecContext(ctx, `UPDATE openai_totp_tasks SET account_id=$1,state=$2,action=$3,error_code=$4,has_candidate=$5,operation=$6,payload=$7,claim_hash=$8,worker_id=$9,lease_until=$10,started_at=$11,updated_at=NOW() WHERE id=$12`, args...)
	return err
}
func (s *openAITOTPStore) Ready(ctx context.Context, now time.Time) (bool, error) {
	var ready bool
	err := s.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM openai_totp_workers WHERE ready_until>$1)`, now).Scan(&ready)
	return ready, err
}
func (s *openAITOTPStore) Heartbeat(ctx context.Context, id string, until time.Time) error {
	// Stale worker IDs have no retained secrets and are pruned as heartbeats arrive.
	if _, err := s.tx.ExecContext(ctx, `DELETE FROM openai_totp_workers WHERE ready_until < NOW()-INTERVAL '1 day'`); err != nil {
		return err
	}
	_, err := s.tx.ExecContext(ctx, `INSERT INTO openai_totp_workers(worker_id,ready_until) VALUES($1,$2) ON CONFLICT(worker_id) DO UPDATE SET ready_until=EXCLUDED.ready_until`, id, until)
	return err
}
