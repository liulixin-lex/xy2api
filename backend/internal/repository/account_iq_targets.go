package repository

import (
	"context"
	"database/sql"
	"time"

	"encoding/json"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain"
)

// IQTargetRepository is the small control-plane port used by the IQ service.
// It intentionally stays separate from AccountRepository.
type IQTargetRepository interface {
	ListIQTargetSummaries(context.Context, int64) ([]domain.IQTargetSummary, error)
	SaveIQTargetsTx(context.Context, *sql.Tx, int64, domain.IQTargetsPatch) error
	ResetIQTargetsOnIdentityChangeTx(context.Context, *sql.Tx, int64) error
	ProjectIQGates(context.Context, *sql.Tx, int64) error
}

func (r *accountRepository) ListIQTargetSummaries(ctx context.Context, accountID int64) ([]domain.IQTargetSummary, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT model,verdict,enabled,verdict_at,last_run_at,next_run_at,paused_reason,lease_until,gate_model FROM account_iq_targets WHERE account_id=$1 ORDER BY model`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.IQTargetSummary
	for rows.Next() {
		var x domain.IQTargetSummary
		var paused string
		var lease *time.Time
		var gate sql.NullString
		if err := rows.Scan(&x.Model, &x.Verdict, &x.Enabled, &x.VerdictAt, &x.LastRunAt, &x.NextRunAt, &paused, &lease, &gate); err != nil {
			return nil, err
		}
		x.GateActive = gate.Valid && gate.String != ""
		switch {
		case lease != nil && lease.After(time.Now().UTC()):
			x.State = "running"
		case paused != "":
			x.State = "paused"
		case x.Enabled:
			x.State = "queued"
		default:
			x.State = "idle"
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r *accountRepository) SaveIQTargetsTx(ctx context.Context, tx *sql.Tx, accountID int64, patch domain.IQTargetsPatch) error {
	for _, model := range patch.Remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_iq_targets WHERE account_id=$1 AND model=$2`, accountID, model); err != nil {
			return err
		}
	}
	for _, s := range patch.Targets {
		if err := s.Validate(); err != nil {
			return err
		}
		enabled := false
		effort := domain.DefaultIQEffort
		interval := 5
		timeout := 120
		if s.Enabled != nil {
			enabled = *s.Enabled
		}
		if s.ReasoningEffort != nil {
			effort = *s.ReasoningEffort
		}
		if s.IntervalMinutes != nil {
			interval = *s.IntervalMinutes
		}
		if s.TimeoutSeconds != nil {
			timeout = *s.TimeoutSeconds
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO account_iq_targets(account_id,model,enabled,reasoning_effort,interval_minutes,timeout_seconds,revision,next_run_at) VALUES($1,$2,$3,$4,$5,$6,$7,CASE WHEN $3 THEN now() ELSE now() END) ON CONFLICT(account_id,model) DO UPDATE SET enabled=EXCLUDED.enabled,reasoning_effort=EXCLUDED.reasoning_effort,interval_minutes=EXCLUDED.interval_minutes,timeout_seconds=EXCLUDED.timeout_seconds,revision=gen_random_uuid()::text,updated_at=now()`, accountID, s.Model, enabled, effort, interval, timeout, uuid.NewString())
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *accountRepository) ResetIQTargetsOnIdentityChangeTx(ctx context.Context, tx *sql.Tx, accountID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE account_iq_targets SET revision=gen_random_uuid()::text,verdict='unknown',verdict_at=NULL,gate_model=NULL,degraded_streak=0,failure_streak=0,paused_reason='',lease_token=NULL,lease_until=NULL,next_run_at=CASE WHEN enabled THEN now() ELSE next_run_at END,updated_at=now() WHERE account_id=$1`, accountID)
	return err
}

func (r *accountRepository) ProjectIQGates(ctx context.Context, tx *sql.Tx, accountID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT gate_model,GREATEST(next_run_at,COALESCE(not_before,next_run_at))+make_interval(secs=>timeout_seconds) FROM account_iq_targets WHERE account_id=$1 AND enabled AND verdict='degraded' AND gate_model IS NOT NULL`, accountID)
	if err != nil {
		return err
	}
	defer rows.Close()
	gates := map[string]time.Time{}
	for rows.Next() {
		var m string
		var t time.Time
		if err := rows.Scan(&m, &t); err != nil {
			return err
		}
		if old, ok := gates[m]; !ok || t.After(old) {
			gates[m] = t
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&raw); err != nil {
		return err
	}
	var extra map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &extra)
	}
	if extra == nil {
		extra = map[string]any{}
	}
	for k := range extra {
		if domain.IsIQGateKey(k) {
			delete(extra, k)
		}
	}
	for model, reset := range gates {
		extra[domain.IQGateKeyPrefix+model] = map[string]any{"rate_limited_at": time.Now().UTC(), "rate_limit_reset_at": reset, "reason": domain.IQGateReason}
	}
	b, _ := json.Marshal(extra)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=$2::jsonb,updated_at=now() WHERE id=$1`, accountID, string(b))
	return err
}

var _ IQTargetRepository = (*accountRepository)(nil)
