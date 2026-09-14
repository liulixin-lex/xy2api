package repository

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	dbent "github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	dbpredicate "github.com/liulixin-lex/xy2api/ent/predicate"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var iqIdentityKeys = []string{"api_key", "base_url", "access_token", "refresh_token", "chatgpt_account_id", "header_override_enabled", "header_overrides", "user_agent"}
var iqTransportExtraKeys = []string{"openai_responses_mode", "openai_responses_supported", "openai_passthrough", "enable_tls_fingerprint", "tls_fingerprint_profile_id"}

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
	state = state.WithSettings(settings)
	state.Status = "unknown"
	state.Reason = ""
	if state.Enabled {
		state.Reason = "configuration_changed"
	}
	state.Revision = uuid.NewString()
	// Keep an in-flight lease until its worker exits, even across disable/re-enable.
	state.NextRunAt = nil
	if state.Enabled {
		state.NextRunAt = &now
	}
	return state
}

func applyIQSettings(state domain.IQCheck, settings *domain.IQCheckSettings, identityChanged bool, now time.Time) domain.IQCheck {
	previous := state.WithSettings(nil)
	updated := previous.WithSettings(settings)
	if identityChanged || previous.Enabled != updated.Enabled || previous.Profile() != updated.Profile() {
		return resetIQState(updated, nil, now)
	}
	if previous.IntervalMinutes != updated.IntervalMinutes && updated.Enabled && (updated.LeaseUntil == nil || !updated.LeaseUntil.After(now)) {
		next := now
		if updated.LastRunAt != nil {
			next = updated.LastRunAt.Add(time.Duration(updated.IntervalMinutes) * time.Minute)
			if next.Before(now) {
				next = now
			}
		}
		updated.NextRunAt = &next
	}
	return updated
}

func (r *accountRepository) mutateIQCheck(ctx context.Context, id int64, fn func(*domain.IQCheck) error) (domain.IQCheck, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return domain.IQCheck{}, err
	}
	defer func() { _ = tx.Rollback() }()
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
		*state = applyIQSettings(*state, &settings, false, time.Now().UTC())
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
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	// Serialize only the short claim transaction to enforce the ten-task limit across replicas.
	if _, err = client.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421021)"); err != nil {
		return nil, err
	}
	recovered, err := client.QueryContext(ctx, `UPDATE accounts SET iq_check = iq_check || jsonb_build_object('status','unknown','reason','interrupted','lease_token','','lease_until',NULL,'next_run_at',CASE WHEN iq_check->>'enabled'='true' THEN to_jsonb($1::timestamptz) ELSE 'null'::jsonb END)
 WHERE deleted_at IS NULL AND iq_check->>'lease_token' <> '' AND (iq_check->>'lease_until')::timestamptz <= $1 RETURNING id`, now)
	if err != nil {
		return nil, err
	}
	recoveredIDs := []int64{}
	for recovered.Next() {
		var id int64
		if err = recovered.Scan(&id); err != nil {
			break
		}
		recoveredIDs = append(recoveredIDs, id)
	}
	if err == nil {
		err = recovered.Err()
	}
	_ = recovered.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range recoveredIDs {
		if _, err = client.ExecContext(ctx, `UPDATE account_iq_check_results SET status='unknown',finished_at=$2,reason='interrupted' WHERE account_id=$1 AND finished_at IS NULL`, id, now); err != nil {
			return nil, err
		}
		if err = enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return nil, err
		}
	}
	rows, err := client.QueryContext(ctx, `SELECT count(*) FROM accounts WHERE deleted_at IS NULL AND (iq_check->>'lease_until')::timestamptz > $1`, now)
	if err != nil {
		return nil, err
	}
	active := 0
	if rows.Next() {
		err = rows.Scan(&active)
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if limit > 10-active {
		limit = 10 - active
	}
	if limit <= 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		for _, id := range recoveredIDs {
			r.syncSchedulerAccountSnapshot(ctx, id)
		}
		return nil, nil
	}
	rows, err = client.QueryContext(ctx, `SELECT id, iq_check, type, extra FROM accounts WHERE deleted_at IS NULL AND platform='openai'
AND iq_check->>'enabled'='true' AND COALESCE((iq_check->>'next_run_at')::timestamptz,'-infinity') <= $1
AND COALESCE((iq_check->>'lease_until')::timestamptz,'-infinity') <= $1
ORDER BY COALESCE((iq_check->>'next_run_at')::timestamptz,'-infinity'),id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, err
	}
	type selected struct {
		id       int64
		state    domain.IQCheck
		protocol string
	}
	var selectedAccounts []selected
	for rows.Next() {
		var item selected
		var raw, extraRaw []byte
		var accountType string
		if err = rows.Scan(&item.id, &raw, &accountType, &extraRaw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &item.state); err != nil {
			break
		}
		var extra map[string]any
		if len(extraRaw) > 0 {
			if err = json.Unmarshal(extraRaw, &extra); err != nil {
				break
			}
		}
		item.protocol = "responses"
		if accountType == service.AccountTypeAPIKey && !openai_compat.ShouldUseResponsesAPI(extra) {
			item.protocol = "chat_completions"
		}
		item.state = item.state.WithSettings(nil)
		selectedAccounts = append(selectedAccounts, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
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
		if _, err = client.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,started_at,prompt_version,grader_version,model,effort,output_mode,protocol,config_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, item.id, state.LeaseToken, now, iqcheck.PromptVersion, iqcheck.GraderVersion, state.Model, state.ReasoningEffort, state.OutputMode, item.protocol, state.Revision); err != nil {
			return nil, err
		}
		if _, err = client.ExecContext(ctx, `DELETE FROM account_iq_check_results WHERE account_id=$1 AND id NOT IN (SELECT id FROM account_iq_check_results WHERE account_id=$1 ORDER BY id DESC LIMIT 2)`, item.id); err != nil {
			return nil, err
		}
		claims = append(claims, service.IQCheckClaim{AccountID: item.id, Token: state.LeaseToken, Revision: state.Revision, StartedAt: now, Profile: state.Profile(), Protocol: item.protocol})
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for _, id := range recoveredIDs {
		r.syncSchedulerAccountSnapshot(ctx, id)
	}
	return claims, nil
}

func (r *accountRepository) CompleteIQCheck(ctx context.Context, claim service.IQCheckClaim, result iqcheck.Result, now time.Time) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
	interval := time.Duration(state.WithSettings(nil).IntervalMinutes) * time.Minute
	jitterMax := min(30*time.Second, interval/20)
	next := now.Add(interval + time.Duration(rand.Int64N(int64(jitterMax)+1)))
	state.NextRunAt = &next
	state.LeaseToken = ""
	state.LeaseUntil = nil
	if _, err = client.Account.UpdateOneID(m.ID).SetIqCheck(state).Save(ctx); err != nil {
		return err
	}
	if _, err = client.ExecContext(ctx, `UPDATE account_iq_check_results SET status=$2,answer=$3,reason=$4,finished_at=$5,latency_ms=$6,normalized_answer=$7,answer_format=$8,format_compliant=$9,reported_model=$10 WHERE lease_token=$1`, claim.Token, result.Status, result.Answer, result.Reason, now, now.Sub(claim.StartedAt).Milliseconds(), result.NormalizedAnswer, result.AnswerFormat, result.FormatCompliant, result.ReportedModel); err != nil {
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
	rows, err := r.sql.QueryContext(ctx, `SELECT id,prompt_version,model,effort,status,answer,reason,started_at,finished_at,latency_ms,normalized_answer,answer_format,output_mode,format_compliant,grader_version,protocol,reported_model,config_revision FROM account_iq_check_results WHERE account_id=$1 ORDER BY id DESC LIMIT 2`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.IQCheckRecord, 0, 2)
	for rows.Next() {
		var item service.IQCheckRecord
		if err = rows.Scan(&item.ID, &item.PromptVersion, &item.Model, &item.Effort, &item.Status, &item.Answer, &item.Reason, &item.StartedAt, &item.FinishedAt, &item.LatencyMS, &item.NormalizedAnswer, &item.AnswerFormat, &item.OutputMode, &item.FormatCompliant, &item.GraderVersion, &item.Protocol, &item.ReportedModel, &item.ConfigRevision); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
