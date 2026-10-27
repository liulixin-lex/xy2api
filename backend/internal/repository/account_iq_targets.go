package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// IQTargetRepository is the control-plane port. Callers that mutate target
// contributions must hold the account row lock before invoking these methods.
type IQTargetRepository interface {
	ListIQTargetSummaries(context.Context, []int64) (map[int64][]domain.IQTargetSummary, error)
	SaveIQTargetsTx(context.Context, *dbent.Client, int64, domain.IQTargetsPatch) (bool, error)
	ResetIQTargetsOnIdentityChangeTx(context.Context, *dbent.Client, int64) (bool, error)
	ProjectIQGates(context.Context, *dbent.Client, int64) (bool, error)
}

func (r *accountRepository) ListIQTargetSummaries(ctx context.Context, ids []int64) (map[int64][]domain.IQTargetSummary, error) {
	out := make(map[int64][]domain.IQTargetSummary)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT account_id,model,verdict,enabled,verdict_at,last_run_at,next_run_at,paused_reason,lease_until,manual_requested_at,gate_model FROM account_iq_targets WHERE account_id = ANY($1) ORDER BY account_id,model`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	for rows.Next() {
		var id int64
		var x domain.IQTargetSummary
		var paused string
		var lease, manual *time.Time
		var gate sql.NullString
		if err := rows.Scan(&id, &x.Model, &x.Verdict, &x.Enabled, &x.VerdictAt, &x.LastRunAt, &x.NextRunAt, &paused, &lease, &manual, &gate); err != nil {
			return nil, err
		}
		x.GateActive = x.Enabled && x.Verdict == "degraded" && gate.Valid && gate.String != ""
		switch {
		case lease != nil && lease.After(now):
			x.State = "running"
		case manual != nil:
			x.State = "queued"
		case x.Enabled && paused != "":
			x.State = "paused"
		default:
			x.State = "idle"
		}
		out[id] = append(out[id], x)
	}
	return out, rows.Err()
}

func (r *accountRepository) SaveIQTargetsTx(ctx context.Context, c *dbent.Client, accountID int64, patch domain.IQTargetsPatch) (bool, error) {
	changed := false
	for _, model := range patch.Remove {
		res, err := c.ExecContext(ctx, `DELETE FROM account_iq_targets WHERE account_id=$1 AND model=$2`, accountID, model)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		changed = changed || n > 0
	}
	for _, s := range patch.Targets {
		if err := s.Validate(); err != nil {
			return false, err
		}
		var id int64
		var oldEnabled bool
		var oldEffort string
		var oldInterval, oldTimeout int
		q, e := c.QueryContext(ctx, `SELECT id,enabled,reasoning_effort,interval_minutes,timeout_seconds FROM account_iq_targets WHERE account_id=$1 AND model=$2 FOR UPDATE`, accountID, s.Model)
		err := sql.ErrNoRows
		if e == nil && q.Next() {
			err = q.Scan(&id, &oldEnabled, &oldEffort, &oldInterval, &oldTimeout)
		}
		if q != nil {
			q.Close()
		}
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		if err == sql.ErrNoRows {
			en := false
			eff := "low"
			iv, to := 5, 120
			if s.Enabled != nil {
				en = *s.Enabled
			}
			if s.ReasoningEffort != nil {
				eff = *s.ReasoningEffort
			}
			if s.IntervalMinutes != nil {
				iv = *s.IntervalMinutes
			}
			if s.TimeoutSeconds != nil {
				to = *s.TimeoutSeconds
			}
			_, err = c.ExecContext(ctx, `INSERT INTO account_iq_targets(account_id,model,enabled,reasoning_effort,interval_minutes,timeout_seconds,revision,next_run_at) VALUES($1,$2,$3,$4,$5,$6,$7,now())`, accountID, s.Model, en, eff, iv, to, uuid.NewString())
			if err != nil {
				return false, err
			}
			changed = true
			continue
		}
		en := oldEnabled
		eff := oldEffort
		iv, to := oldInterval, oldTimeout
		if s.Enabled != nil {
			en = *s.Enabled
		}
		if s.ReasoningEffort != nil {
			eff = *s.ReasoningEffort
		}
		if s.IntervalMinutes != nil {
			iv = *s.IntervalMinutes
		}
		if s.TimeoutSeconds != nil {
			to = *s.TimeoutSeconds
		}
		switchChange := en != oldEnabled
		profileChange := eff != oldEffort || to != oldTimeout
		intervalOnly := iv != oldInterval && !switchChange && !profileChange
		if !switchChange && !profileChange && !intervalOnly {
			continue
		}
		if switchChange {
			_, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=$2,reasoning_effort=$3,interval_minutes=$4,timeout_seconds=$5,revision=gen_random_uuid()::text,verdict='unknown',verdict_at=NULL,gate_model=NULL,paused_reason='',degraded_streak=0,failure_streak=0,not_before=NULL,next_run_at=now(),updated_at=now() WHERE id=$1`, id, en, eff, iv, to)
			if !en {
				_, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET next_run_at=COALESCE(next_run_at,now()) WHERE id=$1`, id)
			}
		} else if profileChange {
			_, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=$2,reasoning_effort=$3,interval_minutes=$4,timeout_seconds=$5,revision=gen_random_uuid()::text,paused_reason='',degraded_streak=0,failure_streak=0,not_before=NULL,next_run_at=now(),updated_at=now() WHERE id=$1`, id, en, eff, iv, to)
		} else if intervalOnly {
			_, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET interval_minutes=$2,next_run_at=LEAST(next_run_at,COALESCE(last_run_at,now())+make_interval(mins=>$2)),updated_at=now() WHERE id=$1`, id, iv)
		} else {
			_, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=$2,reasoning_effort=$3,interval_minutes=$4,timeout_seconds=$5,updated_at=now() WHERE id=$1`, id, en, eff, iv, to)
		}
		if err != nil {
			return false, err
		}
		changed = true
	}
	gateChanged, err := r.ProjectIQGates(ctx, c, accountID)
	return changed || gateChanged, err
}

func (r *accountRepository) ResetIQTargetsOnIdentityChangeTx(ctx context.Context, c *dbent.Client, accountID int64) (bool, error) {
	res, err := c.ExecContext(ctx, `UPDATE account_iq_targets SET revision=gen_random_uuid()::text,verdict='unknown',verdict_at=NULL,gate_model=NULL,degraded_streak=0,failure_streak=0,paused_reason='',not_before=NULL,next_run_at=CASE WHEN enabled THEN now() ELSE next_run_at END,updated_at=now() WHERE account_id=$1`, accountID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	g, e := r.ProjectIQGates(ctx, c, accountID)
	return n > 0 || g, e
}

func (r *accountRepository) ProjectIQGates(ctx context.Context, c *dbent.Client, accountID int64) (bool, error) {
	rows, err := c.QueryContext(ctx, `SELECT gate_model,GREATEST(next_run_at,COALESCE(not_before,next_run_at))+make_interval(secs=>timeout_seconds) FROM account_iq_targets WHERE account_id=$1 AND enabled AND verdict='degraded' AND gate_model IS NOT NULL`, accountID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	desired := map[string]time.Time{}
	for rows.Next() {
		var m string
		var t time.Time
		if err := rows.Scan(&m, &t); err != nil {
			return false, err
		}
		if old, ok := desired[m]; !ok || t.After(old) {
			desired[m] = t
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var raw []byte
	rowsAccount, err := c.QueryContext(ctx, `SELECT extra FROM accounts WHERE id=$1`, accountID)
	if err != nil {
		return false, err
	}
	if !rowsAccount.Next() {
		rowsAccount.Close()
		return false, sql.ErrNoRows
	}
	if err := rowsAccount.Scan(&raw); err != nil {
		rowsAccount.Close()
		return false, err
	}
	rowsAccount.Close()
	if err != nil {
		return false, err
	}
	var extra map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &extra); err != nil {
			return false, fmt.Errorf("decode account extra: %w", err)
		}
	}
	if extra == nil {
		extra = map[string]any{}
	}
	limits := map[string]any{}
	if v, ok := extra["model_rate_limits"]; ok {
		b, e := json.Marshal(v)
		if e != nil {
			return false, e
		}
		if e = json.Unmarshal(b, &limits); e != nil {
			return false, fmt.Errorf("decode model_rate_limits: %w", e)
		}
	}
	currentIQ := map[string]time.Time{}
	for k, v := range limits {
		if domain.IsIQGateKey(k) {
			var item struct {
				Reset time.Time `json:"rate_limit_reset_at"`
			}
			b, _ := json.Marshal(v)
			if json.Unmarshal(b, &item) == nil {
				currentIQ[k] = item.Reset
			}
		}
	}
	same := len(currentIQ) == len(desired)
	for m, t := range desired {
		if old, ok := currentIQ[domain.IQGateKeyPrefix+m]; !ok || !old.Equal(t) {
			same = false
		}
	}
	if same {
		return false, nil
	}
	now := time.Now().UTC()
	for k := range limits {
		if domain.IsIQGateKey(k) {
			delete(limits, k)
		}
	}
	for m, t := range desired {
		limits[domain.IQGateKeyPrefix+m] = map[string]any{"rate_limited_at": now, "rate_limit_reset_at": t, "reason": domain.IQGateReason}
	}
	extra["model_rate_limits"] = limits
	b, e := json.Marshal(extra)
	if e != nil {
		return false, e
	}
	if _, e = c.ExecContext(ctx, `UPDATE accounts SET extra=$2::jsonb,updated_at=now() WHERE id=$1`, accountID, string(b)); e != nil {
		return false, e
	}
	if e = enqueueSchedulerOutbox(ctx, c, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); e != nil {
		return false, e
	}
	return true, nil
}

var _ IQTargetRepository = (*accountRepository)(nil)
