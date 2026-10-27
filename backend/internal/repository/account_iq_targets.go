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
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// IQTargetRepository is the control-plane port. Callers that mutate target
// contributions must hold the account row lock before invoking these methods.
type IQTargetRepository interface {
	ListIQTargetSummaries(context.Context, []int64) (map[int64][]domain.IQTargetSummary, error)
	SaveIQTargetsTx(context.Context, *dbent.Client, int64, domain.IQTargetsPatch) (bool, error)
	ResetIQTargetsOnIdentityChangeTx(context.Context, *dbent.Client, int64) (bool, error)
	ProjectIQGates(context.Context, *dbent.Client, int64) (bool, error)
	ClaimIQTargets(context.Context, time.Time, int) ([]service.IQTargetClaim, error)
	AddIQTargetAttempt(context.Context, service.IQTargetClaim, int, string, iqcheck.Challenge, iqcheck.Result, time.Time, time.Time) error
	CompleteIQTarget(context.Context, service.IQTargetClaim, iqcheck.Result, time.Time) (bool, error)
	CleanupIQResults(context.Context, time.Time, int) (int64, error)
	ListIQResults(context.Context, int64, string, int64, int) (service.IQResultsPage, error)
}

func (r *accountRepository) CleanupIQResults(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 5000
	}
	res, err := r.sql.ExecContext(ctx, `DELETE FROM account_iq_check_results WHERE id IN (SELECT id FROM account_iq_check_results WHERE started_at < $1 ORDER BY id LIMIT $2)`, before, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *accountRepository) ListIQResults(ctx context.Context, accountID int64, model string, beforeID int64, limit int) (service.IQResultsPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{accountID, limit}
	where := `account_id=$1 AND started_at >= now()-interval '7 days'`
	if model != "" {
		args = append(args, model)
		where += fmt.Sprintf(" AND model=$%d", len(args))
	}
	if beforeID > 0 {
		args = append(args, beforeID)
		where += fmt.Sprintf(" AND id<$%d", len(args))
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id,model,outbound_model,mode,effort,status,reason,started_at,finished_at,prompt_version FROM account_iq_check_results WHERE `+where+` ORDER BY id DESC LIMIT $2`, args...)
	if err != nil {
		return service.IQResultsPage{}, err
	}
	defer rows.Close()
	page := service.IQResultsPage{}
	for rows.Next() {
		var x service.IQRound
		if err := rows.Scan(&x.ID, &x.Model, &x.OutboundModel, &x.Mode, &x.Effort, &x.Status, &x.Reason, &x.StartedAt, &x.FinishedAt, &x.PromptVersion); err != nil {
			return page, err
		}
		ar, err := r.sql.QueryContext(ctx, `SELECT attempt_no,effort,challenge,expected,answer,reported_model,model_class,status,reason,latency_ms,diagnostic FROM account_iq_check_attempts WHERE result_id=$1 ORDER BY attempt_no`, x.ID)
		if err != nil {
			return page, err
		}
		for ar.Next() {
			var a service.IQAttempt
			if err := ar.Scan(&a.AttemptNo, &a.Effort, &a.Challenge, &a.Expected, &a.Answer, &a.ReportedModel, &a.ModelClass, &a.Status, &a.Reason, &a.LatencyMS, &a.Diagnostic); err != nil {
				ar.Close()
				return page, err
			}
			x.Attempts = append(x.Attempts, a)
		}
		ar.Close()
		page.Rounds = append(page.Rounds, x)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Rounds) == limit {
		id := page.Rounds[len(page.Rounds)-1].ID
		page.NextBeforeID = &id
	}
	return page, nil
}

func (r *accountRepository) ClaimIQTargets(ctx context.Context, now time.Time, limit int) ([]service.IQTargetClaim, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c := tx.Client()
	if _, err = c.ExecContext(ctx, `SELECT pg_advisory_xact_lock(78421021)`); err != nil {
		return nil, err
	}
	var active int
	countRows, e := c.QueryContext(ctx, `SELECT count(*) FROM account_iq_targets WHERE lease_until > $1`, now)
	if e != nil {
		return nil, e
	}
	if !countRows.Next() {
		countRows.Close()
		return nil, sql.ErrNoRows
	}
	if err = countRows.Scan(&active); err != nil {
		countRows.Close()
		return nil, err
	}
	countRows.Close()
	remaining := limit - active
	if remaining <= 0 {
		return nil, tx.Commit()
	}
	rows, err := c.QueryContext(ctx, `SELECT t.id,t.account_id,t.model,t.revision,t.enabled,t.reasoning_effort,t.timeout_seconds,t.verdict FROM account_iq_targets t JOIN accounts a ON a.id=t.account_id WHERE a.deleted_at IS NULL AND a.platform='openai' AND (t.lease_until IS NULL OR t.lease_until <= $1) AND (t.not_before IS NULL OR t.not_before <= $1) AND (t.manual_requested_at IS NOT NULL OR (t.enabled AND t.paused_reason='' AND t.next_run_at <= $1 AND a.status='active' AND a.schedulable AND COALESCE(a.rate_limit_reset_at,'-infinity') <= $1 AND COALESCE(a.overload_until,'-infinity') <= $1 AND COALESCE(a.temp_unschedulable_until,'-infinity') <= $1)) ORDER BY (t.manual_requested_at IS NULL),COALESCE(t.manual_requested_at,t.next_run_at),t.id LIMIT $2 FOR UPDATE OF t SKIP LOCKED`, now, remaining)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []service.IQTargetClaim
	for rows.Next() {
		var x service.IQTargetClaim
		if err := rows.Scan(&x.ID, &x.AccountID, &x.Model, &x.Revision, &x.Enabled, &x.ReasoningEffort, &x.TimeoutSeconds, &x.Verdict); err != nil {
			return nil, err
		}
		x.LeaseToken = uuid.NewString()
		x.LeaseUntil = now.Add(time.Duration(5*x.TimeoutSeconds+120) * time.Second)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		x := &out[i]
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token=$2,lease_until=$3,manual_requested_at=NULL,updated_at=now() WHERE id=$1`, x.ID, x.LeaseToken, x.LeaseUntil); err != nil {
			return nil, err
		}
		x.Mode = "normal"
		if x.Verdict == "degraded" {
			x.Mode = "recheck"
		}
		if _, err = c.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,prompt_version,model,effort,status,reason,started_at,config_revision,outbound_model,mode) VALUES($1,$2,'candy-v3',$3,$4,'unknown','', $5,$6,$3,$7)`, x.AccountID, x.LeaseToken, x.Model, x.ReasoningEffort, now, x.Revision, x.Mode); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *accountRepository) AddIQTargetAttempt(ctx context.Context, claim service.IQTargetClaim, attempt int, effort string, challenge iqcheck.Challenge, result iqcheck.Result, started, finished time.Time) error {
	b, err := json.Marshal(challenge)
	if err != nil {
		return err
	}
	diagnostic := []byte("null")
	if result.Diagnostic != nil {
		diagnostic = result.Diagnostic.JSON()
	}
	_, err = r.sql.ExecContext(ctx, `INSERT INTO account_iq_check_attempts(result_id,attempt_no,lease_token,started_at,finished_at,status,reason,latency_ms,effort,expected,answer,reported_model,model_class,challenge,diagnostic) SELECT id,$3,$2,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,NULLIF($15::jsonb,'null'::jsonb) FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken, claim.LeaseToken+":"+fmt.Sprint(attempt), attempt, started, finished, result.Status, result.Reason, finished.Sub(started).Milliseconds(), effort, fmt.Sprint(challenge.Answer()), result.Answer, result.ReportedModel, string(result.ModelClass), string(b), string(diagnostic))
	return err
}

func (r *accountRepository) CompleteIQTarget(ctx context.Context, claim service.IQTargetClaim, result iqcheck.Result, now time.Time) (bool, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c := tx.Client()
	if _, err = c.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, claim.AccountID); err != nil {
		return false, err
	}
	rows, err := c.QueryContext(ctx, `SELECT revision,enabled,verdict,COALESCE(gate_model,''),reasoning_effort,interval_minutes,timeout_seconds FROM account_iq_targets WHERE id=$1 AND lease_token=$2 FOR UPDATE`, claim.ID, claim.LeaseToken)
	if err != nil {
		return false, err
	}
	var revision, verdict, gate, effort string
	var enabled bool
	var interval, timeout int
	if !rows.Next() {
		rows.Close()
		_, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status='unknown',reason='lease_lost' WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, now)
		if err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err = rows.Scan(&revision, &enabled, &verdict, &gate, &effort, &interval, &timeout); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if revision != claim.Revision {
		_, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status='unknown',reason='cancelled_by_account_change' WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, now)
		if err != nil {
			return false, err
		}
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if !enabled {
		result.Status = "unknown"
		result.Reason = "cancelled_by_account_change"
	}
	newVerdict := verdict
	if result.Status == "smart" || result.Status == "degraded" {
		newVerdict = result.Status
	}
	newGate := gate
	if result.Status == "smart" {
		newGate = ""
	}
	if result.Status == "degraded" {
		newGate = claim.Model
	}
	next := now.Add(time.Duration(interval) * time.Minute)
	if result.Status == "degraded" {
		next = now.Add(time.Hour)
	}
	if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET verdict=$3,verdict_at=CASE WHEN $3='unknown' THEN verdict_at ELSE $4 END,gate_model=NULLIF($5,''),last_run_at=$4,last_result=$6,last_reason=$7,next_run_at=$8,lease_token=NULL,lease_until=NULL,manual_requested_at=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken, newVerdict, now, newGate, result.Status, result.Reason, next); err != nil {
		return false, err
	}
	changed, err := r.ProjectIQGates(ctx, c, claim.AccountID)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	if changed {
		r.syncSchedulerAccountSnapshot(ctx, claim.AccountID)
	}
	return changed, nil
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
			_, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET enabled=$2,reasoning_effort=$3,interval_minutes=$4,timeout_seconds=$5,revision=gen_random_uuid()::text,verdict='unknown',verdict_at=NULL,gate_model=NULL,paused_reason='',degraded_streak=0,failure_streak=0,not_before=NULL,next_run_at=CASE WHEN $2 THEN now() ELSE next_run_at END,updated_at=now() WHERE id=$1`, id, en, eff, iv, to)
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
