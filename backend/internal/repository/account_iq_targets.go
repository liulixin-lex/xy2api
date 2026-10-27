package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var iqTransportExtraKeys = []string{"openai_responses_mode", "openai_responses_supported", "openai_passthrough", "enable_tls_fingerprint", "tls_fingerprint_profile_id"}

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
	SetIQTargetOutboundModel(context.Context, service.IQTargetClaim, string) (bool, error)
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

func applyIQStatusFilter(ctx context.Context, q *dbent.AccountQuery) *dbent.AccountQuery {
	status := service.IQStatusFilter(ctx)
	if status == "" {
		return q
	}
	allowed := map[string]bool{"smart": true, "degraded": true, "unknown": true}
	if !allowed[status] {
		return q
	}
	return q.Where(dbaccount.PlatformEQ(service.PlatformOpenAI), func(s *entsql.Selector) {
		expr := "EXISTS (SELECT 1 FROM account_iq_targets t WHERE t.account_id=" + s.C("id") + " AND t.enabled AND "
		switch status {
		case "degraded":
			expr += "t.verdict='degraded'"
		case "smart":
			expr += "t.verdict='smart' AND NOT EXISTS (SELECT 1 FROM account_iq_targets u WHERE u.account_id=t.account_id AND u.enabled AND u.verdict<>'smart')"
		default:
			expr = "(NOT EXISTS (SELECT 1 FROM account_iq_targets d WHERE d.account_id=" + s.C("id") + " AND d.enabled AND d.verdict='degraded') AND NOT (EXISTS (SELECT 1 FROM account_iq_targets sm WHERE sm.account_id=" + s.C("id") + " AND sm.enabled) AND NOT EXISTS (SELECT 1 FROM account_iq_targets u WHERE u.account_id=" + s.C("id") + " AND u.enabled AND u.verdict<>'smart'))"
		}
		s.Where(entsql.ExprP(expr + ")"))
	})
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
	page := service.IQResultsPage{Rounds: make([]service.IQRound, 0)}
	for rows.Next() {
		var x service.IQRound
		x.Attempts = make([]service.IQAttempt, 0)
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
			a.Answer = iqcheck.ExtractAnswer(a.Answer).Value
			if len(a.Answer) > 64 {
				a.Answer = ""
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

func iqDatabaseTime(ctx context.Context, c *dbent.Client) (time.Time, error) {
	rows, err := c.QueryContext(ctx, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return time.Time{}, sql.ErrNoRows
	}
	var now time.Time
	if err := rows.Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now, rows.Err()
}

func (r *accountRepository) ClaimIQTargets(ctx context.Context, _ time.Time, limit int) ([]service.IQTargetClaim, error) {
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
	now, err := iqDatabaseTime(ctx, c)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	_, _ = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$1,status='unknown',reason='interrupted' WHERE finished_at IS NULL AND NOT EXISTS (SELECT 1 FROM account_iq_targets t WHERE t.lease_token=account_iq_check_results.lease_token AND t.lease_until>$1)`, now)
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
	rows, err := c.QueryContext(ctx, `SELECT t.id,t.account_id,t.model,t.revision,t.enabled,t.reasoning_effort,t.timeout_seconds,t.verdict,t.degraded_streak,t.failure_streak,t.manual_requested_at FROM account_iq_targets t JOIN accounts a ON a.id=t.account_id WHERE a.deleted_at IS NULL AND a.platform='openai' AND (t.lease_until IS NULL OR t.lease_until <= $1) AND (t.not_before IS NULL OR t.not_before <= $1) AND (t.manual_requested_at IS NOT NULL OR (t.enabled AND t.paused_reason='' AND t.next_run_at <= $1 AND a.status='active' AND a.schedulable AND COALESCE(a.rate_limit_reset_at,'-infinity') <= $1 AND COALESCE(a.overload_until,'-infinity') <= $1 AND COALESCE(a.temp_unschedulable_until,'-infinity') <= $1)) ORDER BY (t.manual_requested_at IS NULL),COALESCE(t.manual_requested_at,t.next_run_at),t.id LIMIT $2 FOR UPDATE OF t SKIP LOCKED`, now, remaining)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []service.IQTargetClaim
	for rows.Next() {
		var x service.IQTargetClaim
		if err := rows.Scan(&x.ID, &x.AccountID, &x.Model, &x.Revision, &x.Enabled, &x.ReasoningEffort, &x.TimeoutSeconds, &x.Verdict, &x.DegradedStreak, &x.FailureStreak, &x.ManualRequestedAt); err != nil {
			return nil, err
		}
		x.ManualRequested = x.ManualRequestedAt != nil
		x.LeaseToken = uuid.NewString()
		x.LeaseUntil = now.Add(time.Duration(5*x.TimeoutSeconds+120) * time.Second)
		x.OutboundModel = x.Model
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

func (r *accountRepository) SetIQTargetOutboundModel(ctx context.Context, claim service.IQTargetClaim, model string) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `UPDATE account_iq_check_results r SET outbound_model=$3 FROM account_iq_targets t WHERE r.lease_token=$1 AND r.finished_at IS NULL AND t.id=$2 AND t.lease_token=$1 AND t.revision=$4 AND t.lease_until>clock_timestamp()`, claim.LeaseToken, claim.ID, model, claim.Revision)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (r *accountRepository) DeferIQTarget(ctx context.Context, claim service.IQTargetClaim, reason string, next time.Time) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c := tx.Client()
	rows, err := c.QueryContext(ctx, `SELECT revision FROM account_iq_targets WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() FOR UPDATE`, claim.ID, claim.LeaseToken)
	if err != nil {
		return err
	}
	if !rows.Next() {
		rows.Close()
		return tx.Commit()
	}
	var revision string
	if err := rows.Scan(&revision); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err = c.ExecContext(ctx, `DELETE FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken); err != nil {
		return err
	}
	if revision != claim.Revision {
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token=NULL,lease_until=NULL,next_run_at=$3,not_before=$3,manual_requested_at=CASE WHEN $4 THEN COALESCE(manual_requested_at,$5) ELSE manual_requested_at END,last_reason=$6,updated_at=now() WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken, next, claim.ManualRequested, claim.ManualRequestedAt, reason); err != nil {
		return err
	}
	return tx.Commit()
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
	answer := ""
	if result.NormalizedAnswer != nil {
		answer = *result.NormalizedAnswer
		if len(answer) > 64 {
			answer = ""
		}
	}
	_, err = r.sql.ExecContext(ctx, `INSERT INTO account_iq_check_attempts(result_id,attempt_no,lease_token,started_at,finished_at,status,reason,latency_ms,effort,expected,answer,reported_model,model_class,challenge,diagnostic) SELECT id,$3,$2,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,NULLIF($15::jsonb,'null'::jsonb) FROM account_iq_check_results WHERE lease_token=$1`, claim.LeaseToken, claim.LeaseToken+":"+fmt.Sprint(attempt), attempt, started, finished, result.Status, result.Reason, finished.Sub(started).Milliseconds(), effort, fmt.Sprint(challenge.Answer()), answer, result.ReportedModel, string(result.ModelClass), string(b), string(diagnostic))
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
	rows, err := c.QueryContext(ctx, `SELECT revision,enabled,verdict,COALESCE(gate_model,''),reasoning_effort,interval_minutes,timeout_seconds,lease_until FROM account_iq_targets WHERE id=$1 AND lease_token=$2 FOR UPDATE`, claim.ID, claim.LeaseToken)
	if err != nil {
		return false, err
	}
	var revision, verdict, gate, effort string
	var enabled bool
	var interval, timeout int
	var leaseUntil time.Time
	if !rows.Next() {
		rows.Close()
		completedAt, timeErr := iqDatabaseTime(ctx, c)
		if timeErr != nil {
			return false, timeErr
		}
		_, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status='unknown',reason='lease_lost' WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, completedAt)
		if err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err = rows.Scan(&revision, &enabled, &verdict, &gate, &effort, &interval, &timeout, &leaseUntil); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	completedAt, err := iqDatabaseTime(ctx, c)
	if err != nil {
		return false, err
	}
	if !leaseUntil.After(completedAt) {
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status='unknown',reason='lease_lost' WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, completedAt); err != nil {
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
	if revision != claim.Revision {
		_, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status='unknown',reason='cancelled_by_account_change' WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, completedAt)
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
	currentEntity, err := c.Account.Query().Where(dbaccount.IDEQ(claim.AccountID)).WithProxy().Only(ctx)
	if err != nil {
		return false, err
	}
	currentAccount := accountEntityToService(currentEntity)
	if currentEntity.Edges.Proxy != nil {
		currentAccount.Proxy = proxyEntityToService(currentEntity.Edges.Proxy)
	}
	if !service.CodexTicketIQIdentitiesCurrent(currentAccount, claim.TicketIdentities, completedAt) {
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status='unknown',reason='cancelled_by_ticket_change' WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, completedAt); err != nil {
			return false, err
		}
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET lease_token=NULL,lease_until=NULL,next_run_at=$3,not_before=NULL WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken, completedAt); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	manual := claim.ManualRequested
	if !enabled && !manual {
		result.Status = "unknown"
		result.Reason = "cancelled_by_account_change"
	}
	if _, err = c.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,status=$3,reason=$4 WHERE lease_token=$1 AND finished_at IS NULL`, claim.LeaseToken, completedAt, result.Status, result.Reason); err != nil {
		return false, err
	}
	if manual && !enabled {
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET last_run_at=$3,last_result=$4,last_reason=$5,lease_token=NULL,lease_until=NULL,manual_requested_at=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken, completedAt, result.Status, result.Reason); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
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
		newGate = claim.OutboundModel
	}
	sched := iqcheck.NextSchedule(completedAt, time.Duration(interval)*time.Minute, claim.DegradedStreak, claim.FailureStreak, result, 0)
	next := completedAt.Add(time.Duration(interval) * time.Minute)
	if sched.NextRunAt != nil {
		next = *sched.NextRunAt
	}
	if sched.NotBefore != nil && sched.NotBefore.After(next) {
		next = *sched.NotBefore
	}
	paused := sched.PausedReason
	if manual && result.Status != "unknown" {
		paused = ""
	}
	if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET verdict=$3,verdict_at=CASE WHEN $3='unknown' THEN verdict_at ELSE $4 END,gate_model=NULLIF($5,''),degraded_streak=$6,failure_streak=$7,last_run_at=$4,last_result=$8,last_reason=$9,next_run_at=$10,not_before=$11,paused_reason=$12,lease_token=NULL,lease_until=NULL,manual_requested_at=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2`, claim.ID, claim.LeaseToken, newVerdict, completedAt, newGate, sched.DegradedStreak, sched.FailureStreak, result.Status, result.Reason, next, sched.NotBefore, paused); err != nil {
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
	return true, nil
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
		case x.Enabled && x.NextRunAt != nil && !x.NextRunAt.After(now):
			x.State = "queued"
		default:
			x.State = "idle"
		}
		out[id] = append(out[id], x)
	}
	return out, rows.Err()
}

func (r *accountRepository) ListIQTargetViews(ctx context.Context, id int64) ([]domain.IQTargetView, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT model,enabled,reasoning_effort,interval_minutes,timeout_seconds,verdict,verdict_at,gate_model,last_run_at,last_result,last_reason,next_run_at,not_before,paused_reason,lease_until,manual_requested_at,COALESCE((SELECT id FROM account_iq_check_results r WHERE r.account_id=account_iq_targets.account_id AND r.model=account_iq_targets.model AND r.finished_at IS NULL ORDER BY r.id DESC LIMIT 1),0),COALESCE((SELECT count(*) FROM account_iq_check_attempts a JOIN account_iq_check_results r ON r.id=a.result_id WHERE r.account_id=account_iq_targets.account_id AND r.model=account_iq_targets.model AND r.finished_at IS NULL),0) FROM account_iq_targets WHERE account_id=$1 ORDER BY model`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.IQTargetView
	for rows.Next() {
		var x domain.IQTargetView
		var gate sql.NullString
		var lease, manual *time.Time
		var round int64
		var attempts int
		if err := rows.Scan(&x.Model, &x.Enabled, &x.ReasoningEffort, &x.IntervalMinutes, &x.TimeoutSeconds, &x.Verdict, &x.VerdictAt, &gate, &x.LastRunAt, &x.LastResult, &x.LastReason, &x.NextRunAt, &x.NotBefore, &x.PausedReason, &lease, &manual, &round, &attempts); err != nil {
			return nil, err
		}
		if gate.Valid {
			x.GateModel = &gate.String
			x.GateActive = x.Enabled && x.Verdict == "degraded"
			if x.GateActive {
				now := time.Now().UTC()
				t := now.Add(time.Duration(x.TimeoutSeconds) * time.Second)
				if x.NextRunAt != nil && x.NextRunAt.After(now) {
					t = x.NextRunAt.Add(time.Duration(x.TimeoutSeconds) * time.Second)
				}
				if x.NotBefore != nil && x.NotBefore.After(now) && x.NotBefore.Add(time.Duration(x.TimeoutSeconds)*time.Second).After(t) {
					t = x.NotBefore.Add(time.Duration(x.TimeoutSeconds) * time.Second)
				}
				x.GateResetAt = &t
			}
		}
		if round > 0 {
			x.RoundID = &round
		}
		x.CompletedAttempts = attempts
		switch {
		case lease != nil && lease.After(time.Now().UTC()):
			x.State = "running"
		case manual != nil:
			x.State = "queued"
		case x.PausedReason != "":
			x.State = "paused"
		case x.Enabled && x.NextRunAt != nil && !x.NextRunAt.After(time.Now().UTC()):
			x.State = "queued"
		default:
			x.State = "idle"
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		for _, m := range domain.DefaultIQModels {
			out = append(out, domain.IQTargetView{Model: m, ReasoningEffort: domain.DefaultIQEffort, IntervalMinutes: 5, TimeoutSeconds: 120, Verdict: "unknown", State: "idle"})
		}
	}
	return out, nil
}
func (r *accountRepository) SaveIQTargets(ctx context.Context, id int64, patch domain.IQTargetsPatch) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c := tx.Client()
	if _, err = c.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, id); err != nil {
		return err
	}
	if _, err = r.SaveIQTargetsTx(ctx, c, id, patch); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *accountRepository) QueueIQTargets(ctx context.Context, accountID int64, models []string) ([]service.IQRunTarget, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT model,lease_until FROM account_iq_targets WHERE account_id=$1 AND ($2::text[] IS NULL OR model=ANY($2)) ORDER BY model`, accountID, pq.Array(models))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []service.IQRunTarget
	seen := map[string]bool{}
	for rows.Next() {
		var model string
		var lease *time.Time
		if err := rows.Scan(&model, &lease); err != nil {
			return nil, err
		}
		state := "queued"
		if lease != nil && lease.After(time.Now().UTC()) {
			state = "running"
		}
		out = append(out, service.IQRunTarget{Model: model, State: state})
		seen[model] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err = r.sql.ExecContext(ctx, `UPDATE account_iq_targets SET manual_requested_at=COALESCE(manual_requested_at,now()) WHERE account_id=$1 AND ($2::text[] IS NULL OR model=ANY($2)) AND (lease_until IS NULL OR lease_until<=now())`, accountID, pq.Array(models)); err != nil {
		return nil, err
	}
	for _, model := range models {
		if !seen[model] {
			out = append(out, service.IQRunTarget{Model: model, State: "rejected", Reason: "unknown_target"})
		}
	}
	return out, nil
}

func (r *accountRepository) ReconcileIQGates(ctx context.Context) error {
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM accounts WHERE EXISTS (SELECT 1 FROM account_iq_targets t WHERE t.account_id=accounts.id AND t.enabled AND t.verdict='degraded') OR EXISTS (SELECT 1 FROM jsonb_object_keys(CASE WHEN jsonb_typeof(extra->'model_rate_limits')='object' THEN extra->'model_rate_limits' ELSE '{}'::jsonb END) AS limits(key) WHERE key LIKE 'iq:%')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		tx, err := r.client.Tx(ctx)
		if err != nil {
			return err
		}
		c := tx.Client()
		if _, err = c.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, id); err != nil {
			tx.Rollback()
			return err
		}
		missingRows, err := c.QueryContext(ctx, `SELECT id,model FROM account_iq_targets WHERE account_id=$1 AND enabled AND verdict='degraded' AND gate_model IS NULL ORDER BY id FOR UPDATE`, id)
		if err != nil {
			tx.Rollback()
			return err
		}
		type missingGate struct {
			id    int64
			model string
		}
		var missing []missingGate
		for missingRows.Next() {
			var target missingGate
			if err = missingRows.Scan(&target.id, &target.model); err != nil {
				missingRows.Close()
				tx.Rollback()
				return err
			}
			missing = append(missing, target)
		}
		if err = missingRows.Err(); err != nil {
			missingRows.Close()
			tx.Rollback()
			return err
		}
		missingRows.Close()
		if len(missing) > 0 {
			entity, loadErr := c.Account.Query().Where(dbaccount.IDEQ(id)).WithProxy().Only(ctx)
			if loadErr != nil {
				tx.Rollback()
				return loadErr
			}
			account := accountEntityToService(entity)
			if entity.Edges.Proxy != nil {
				account.Proxy = proxyEntityToService(entity.Edges.Proxy)
			}
			for _, target := range missing {
				outboundModel := service.CanonicalIQOutboundModel(account, target.model)
				if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET gate_model=$2,updated_at=now() WHERE id=$1 AND gate_model IS NULL`, target.id, outboundModel); err != nil {
					tx.Rollback()
					return err
				}
			}
		}
		if _, err := r.ProjectIQGates(ctx, c, id); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
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

func (r *accountRepository) RequeueIQTargetsTx(ctx context.Context, c *dbent.Client, account *service.Account, outboundModel string) (bool, error) {
	rows, err := c.QueryContext(ctx, `SELECT id,model,enabled FROM account_iq_targets WHERE account_id=$1 AND enabled ORDER BY model FOR UPDATE`, account.ID)
	if err != nil {
		return false, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var model string
		var enabled bool
		if err := rows.Scan(&id, &model, &enabled); err != nil {
			rows.Close()
			return false, err
		}
		if enabled && service.CanonicalIQOutboundModel(account, model) == outboundModel {
			ids = append(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if len(ids) == 0 {
		return false, nil
	}
	for _, id := range ids {
		if _, err = c.ExecContext(ctx, `UPDATE account_iq_targets SET revision=gen_random_uuid()::text,paused_reason='',next_run_at=now(),not_before=NULL,updated_at=now() WHERE id=$1`, id); err != nil {
			return false, err
		}
	}
	changed, err := r.ProjectIQGates(ctx, c, account.ID)
	if err != nil {
		return false, err
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	rows, err = c.QueryContext(ctx, `SELECT extra->'model_rate_limits' FROM accounts WHERE id=$1`, account.ID)
	if err != nil {
		return false, err
	}
	var raw []byte
	if !rows.Next() {
		rows.Close()
		return false, sql.ErrNoRows
	}
	if err = rows.Scan(&raw); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	var currentLimits map[string]any
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &currentLimits); err != nil {
			return false, err
		}
	}
	if currentLimits == nil {
		currentLimits = map[string]any{}
	}
	incomingLimits := map[string]any{}
	if value, ok := account.Extra["model_rate_limits"]; ok {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return false, marshalErr
		}
		if err = json.Unmarshal(encoded, &incomingLimits); err != nil {
			return false, err
		}
	}
	if incomingLimits == nil {
		incomingLimits = map[string]any{}
	}
	for key := range incomingLimits {
		if domain.IsIQGateKey(key) {
			delete(incomingLimits, key)
		}
	}
	for key, value := range currentLimits {
		if domain.IsIQGateKey(key) {
			incomingLimits[key] = value
		}
	}
	if len(incomingLimits) > 0 {
		account.Extra["model_rate_limits"] = incomingLimits
	} else {
		delete(account.Extra, "model_rate_limits")
	}
	if !changed {
		if err = enqueueSchedulerOutbox(ctx, c, service.SchedulerOutboxEventAccountChanged, &account.ID, nil, nil); err != nil {
			return false, err
		}
	}
	return true, nil
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
	if limits == nil {
		limits = map[string]any{}
	}
	currentIQ := map[string]time.Time{}
	for k, v := range limits {
		if domain.IsIQGateKey(k) {
			var item struct {
				Reset time.Time `json:"rate_limit_reset_at"`
			}
			b, _ := json.Marshal(v)
			if json.Unmarshal(b, &item) != nil || item.Reset.IsZero() {
				currentIQ[k] = time.Unix(0, 0).UTC()
			} else {
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
