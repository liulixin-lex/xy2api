package repository

import (
	"context"
	"encoding/json"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	dbent "github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	dbpredicate "github.com/liulixin-lex/xy2api/ent/predicate"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var iqIdentityKeys = []string{"api_key", "base_url", "access_token", "refresh_token", "chatgpt_account_id", "header_override_enabled", "header_overrides"}

func iqSchedulablePredicate() dbpredicate.Account {
	return func(s *entsql.Selector) {
		s.Where(entsql.ExprP("NOT (COALESCE(" + s.C("iq_check") + "->>'enabled','false') = 'true' AND " + "COALESCE(" + s.C("iq_check") + "->>'status','unknown') = 'degraded')"))
	}
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
		s.Where(entsql.ExprP("CASE WHEN " + s.C("iq_check") + "->>'enabled' = 'true' THEN COALESCE(" + s.C("iq_check") + "->>'status','unknown') ELSE 'unknown' END = '" + status + "'"))
	})
}

func resetIQState(state domain.IQCheck, settings *domain.IQCheckSettings, now time.Time) domain.IQCheck {
	if settings != nil {
		state.Enabled = settings.Enabled
		state.IntervalMinutes = settings.IntervalMinutes
	}
	if state.IntervalMinutes == 0 {
		state.IntervalMinutes = 15
	}
	state.Status = "unknown"
	state.Reason = ""
	state.Revision = uuid.NewString()
	// Keep an in-flight lease until its worker exits, even across disable/re-enable.
	state.NextRunAt = nil
	if state.Enabled {
		state.NextRunAt = &now
	}
	return state
}

func (r *accountRepository) mutateIQCheck(ctx context.Context, id int64, fn func(*domain.IQCheck) error) (domain.IQCheck, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return domain.IQCheck{}, err
	}
	defer tx.Rollback()
	client := tx.Client()
	m, err := client.Account.Query().Where(dbaccount.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		return domain.IQCheck{}, translatePersistenceError(err, service.ErrAccountNotFound, nil)
	}
	if m.Platform != service.PlatformOpenAI {
		return domain.IQCheck{}, service.ErrIQCheckInvalid
	}
	state := m.IqCheck
	if err := fn(&state); err != nil {
		return state, err
	}
	if _, err = client.Account.UpdateOneID(id).SetIqCheck(state).Save(ctx); err != nil {
		return state, err
	}
	if err = enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return state, err
	}
	if err = tx.Commit(); err != nil {
		return state, err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return state.Summary(), nil
}

func (r *accountRepository) ConfigureIQCheck(ctx context.Context, id int64, settings domain.IQCheckSettings) (domain.IQCheck, error) {
	if err := service.ValidateIQCheckSettings(service.PlatformOpenAI, &settings); err != nil {
		return domain.IQCheck{}, err
	}
	return r.mutateIQCheck(ctx, id, func(state *domain.IQCheck) error {
		if state.Enabled == settings.Enabled && state.IntervalMinutes == settings.IntervalMinutes {
			return nil
		}
		*state = resetIQState(*state, &settings, time.Now().UTC())
		return nil
	})
}

func (r *accountRepository) QueueIQCheck(ctx context.Context, id int64) error {
	_, err := r.mutateIQCheck(ctx, id, func(state *domain.IQCheck) error {
		if !state.Enabled {
			return service.ErrIQCheckDisabled
		}
		now := time.Now().UTC()
		if state.LeaseUntil == nil || !state.LeaseUntil.After(now) {
			state.NextRunAt = &now
		}
		return nil
	})
	return err
}

func (r *accountRepository) ClaimIQChecks(ctx context.Context, now time.Time, limit int) ([]service.IQCheckClaim, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	client := tx.Client()
	// Serialize only the short claim transaction to enforce the ten-task limit across replicas.
	if _, err = client.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421021)"); err != nil {
		return nil, err
	}
	rows, err := client.QueryContext(ctx, `SELECT count(*) FROM accounts WHERE deleted_at IS NULL AND (iq_check->>'lease_until')::timestamptz > $1`, now)
	if err != nil {
		return nil, err
	}
	active := 0
	if rows.Next() {
		err = rows.Scan(&active)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if limit > 10-active {
		limit = 10 - active
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err = client.QueryContext(ctx, `SELECT id, iq_check FROM accounts WHERE deleted_at IS NULL AND platform='openai'
AND iq_check->>'enabled'='true' AND COALESCE((iq_check->>'next_run_at')::timestamptz,'-infinity') <= $1
AND COALESCE((iq_check->>'lease_until')::timestamptz,'-infinity') <= $1
ORDER BY COALESCE((iq_check->>'next_run_at')::timestamptz,'-infinity'),id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, err
	}
	type selected struct {
		id    int64
		state domain.IQCheck
	}
	var selectedAccounts []selected
	for rows.Next() {
		var item selected
		var raw []byte
		if err = rows.Scan(&item.id, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &item.state); err != nil {
			break
		}
		selectedAccounts = append(selectedAccounts, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	claims := make([]service.IQCheckClaim, 0, len(selectedAccounts))
	for _, item := range selectedAccounts {
		state := item.state
		if state.Revision == "" {
			state.Revision = uuid.NewString()
		}
		state.LeaseToken = uuid.NewString()
		until := now.Add(180 * time.Second)
		state.LeaseUntil = &until
		if _, err = client.Account.UpdateOneID(item.id).SetIqCheck(state).Save(ctx); err != nil {
			return nil, err
		}
		if _, err = client.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,reason='interrupted' WHERE account_id=$1 AND finished_at IS NULL`, item.id, now); err != nil {
			return nil, err
		}
		if _, err = client.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,started_at) VALUES($1,$2,$3)`, item.id, state.LeaseToken, now); err != nil {
			return nil, err
		}
		if _, err = client.ExecContext(ctx, `DELETE FROM account_iq_check_results WHERE account_id=$1 AND id NOT IN (SELECT id FROM account_iq_check_results WHERE account_id=$1 ORDER BY id DESC LIMIT 2)`, item.id); err != nil {
			return nil, err
		}
		claims = append(claims, service.IQCheckClaim{AccountID: item.id, Token: state.LeaseToken, Revision: state.Revision, StartedAt: now})
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (r *accountRepository) CompleteIQCheck(ctx context.Context, claim service.IQCheckClaim, result iqcheck.Result, now time.Time) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	client := tx.Client()
	m, err := client.Account.Query().Where(dbaccount.IDEQ(claim.AccountID)).ForUpdate().Only(ctx)
	if dbent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	state := m.IqCheck
	if state.LeaseToken != claim.Token {
		return nil
	}
	if !state.Enabled || state.Revision != claim.Revision {
		state.LeaseToken = ""
		state.LeaseUntil = nil
		if _, err = client.Account.UpdateOneID(m.ID).SetIqCheck(state).Save(ctx); err != nil {
			return err
		}
		if _, err = client.ExecContext(ctx, `UPDATE account_iq_check_results SET reason='cancelled_by_account_change',finished_at=$2,latency_ms=$3 WHERE lease_token=$1`, claim.Token, now, now.Sub(claim.StartedAt).Milliseconds()); err != nil {
			return err
		}
		return tx.Commit()
	}
	state.Status = result.Status
	state.Reason = result.Reason
	state.LastRunAt = &now
	next := now.Add(time.Duration(state.IntervalMinutes) * time.Minute)
	state.NextRunAt = &next
	state.LeaseToken = ""
	state.LeaseUntil = nil
	if _, err = client.Account.UpdateOneID(m.ID).SetIqCheck(state).Save(ctx); err != nil {
		return err
	}
	if _, err = client.ExecContext(ctx, `UPDATE account_iq_check_results SET status=$2,answer=$3,reason=$4,finished_at=$5,latency_ms=$6 WHERE lease_token=$1`, claim.Token, result.Status, result.Answer, result.Reason, now, now.Sub(claim.StartedAt).Milliseconds()); err != nil {
		return err
	}
	if err = enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &m.ID, nil, nil); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.syncSchedulerAccountSnapshot(ctx, m.ID)
	return nil
}

func (r *accountRepository) ListIQCheckRecords(ctx context.Context, id int64) ([]service.IQCheckRecord, error) {
	a, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Platform != service.PlatformOpenAI {
		return nil, service.ErrIQCheckInvalid
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id,prompt_version,model,effort,status,answer,reason,started_at,finished_at,latency_ms FROM account_iq_check_results WHERE account_id=$1 ORDER BY id DESC LIMIT 2`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.IQCheckRecord, 0, 2)
	for rows.Next() {
		var item service.IQCheckRecord
		if err = rows.Scan(&item.ID, &item.PromptVersion, &item.Model, &item.Effort, &item.Status, &item.Answer, &item.Reason, &item.StartedAt, &item.FinishedAt, &item.LatencyMS); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
