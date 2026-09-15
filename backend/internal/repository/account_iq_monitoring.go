package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func finishIQSchedule(s *domain.IQCheck, r iqcheck.Result, now time.Time) {
	s.LastRunStatus, s.LastRunReason = r.Status, r.Reason
	if r.Status == "smart" || r.Status == "degraded" {
		s.Status = r.Status
		s.Reason = r.Reason
		s.LastValidAt = &now
	}
	s.RoundStartedAt = nil
	s.RoundID = ""
	s.RoundDeadline = nil
	s.RetryAt = nil
	s.LastRunAt = &now
	s.StartedAt = nil
	s.ExecutionState = "idle"
	s.ExecutionReason = ""
	s.BusyDeferrals = 0
	s.TaskID = ""
	if r.Status == "smart" {
		s.SmartStreak = min(6, s.SmartStreak+1)
	} else {
		s.SmartStreak = 0
	}
	pause, transient, protocol := iqcheck.FailurePolicy(r)
	if transient {
		s.FailureStreak = min(3, s.FailureStreak+1)
	} else {
		s.FailureStreak = 0
	}
	if protocol {
		s.ProtocolFailures = min(4, s.ProtocolFailures+1)
	} else {
		s.ProtocolFailures = 0
	}
	interval := s.EffectiveInterval()
	if transient {
		interval = min(time.Duration(1<<max(0, s.FailureStreak-1))*interval, max(interval, time.Hour))
	}
	next := now.Add(interval)
	if r.Diagnostic != nil {
		d := r.Diagnostic
		if d.RetryAfter != nil && (s.NotBefore == nil || d.RetryAfter.After(*s.NotBefore)) {
			s.NotBefore = d.RetryAfter
		}
		pause = pause || d.RetryAfterUnbounded
		if d.RetryAfterUnbounded {
			bound := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
			s.NotBefore = &bound
		}
	}
	if s.NotBefore != nil && s.NotBefore.After(next) {
		next = *s.NotBefore
	}
	s.NextRunAt = &next
	if s.ProtocolFailures >= 3 {
		delay := 30 * time.Minute
		if s.ProtocolFailures >= 4 {
			delay = time.Hour
		}
		next = now.Add(delay)
		if s.NotBefore != nil {
			next = iqLater(next, *s.NotBefore)
		}
		s.NotBefore = &next
		s.NextRunAt = &next
		s.ExecutionState = "deferred"
		s.ExecutionReason = "protocol_cooldown"
	}
	if pause {
		s.ExecutionState = "paused"
		s.ExecutionReason = r.Reason
		s.NextRunAt = nil
	}
	s.LeaseToken = ""
	s.LeaseUntil = nil
}

// DeferIQCheck releases an unsent reservation without changing the assessment or records.
func (r *accountRepository) DeferIQCheck(ctx context.Context, c service.IQCheckClaim, reason string, next time.Time) error {
	_, err := r.mutateIQCheck(ctx, c.AccountID, func(s *domain.IQCheck) error {
		if s.LeaseToken != c.Token || s.StartedAt != nil {
			return nil
		}
		s.LeaseToken = ""
		s.LeaseUntil = nil
		if s.Revision != c.Revision || !s.Enabled {
			if s.Enabled {
				queueIQ(s, time.Now().UTC())
			}
			return nil
		}
		// Preserve constraints that may have changed since the worker read this account.
		if earliest, constraint := s.Eligibility(time.Now().UTC()); earliest.After(next) {
			next, reason = earliest, constraint
		}
		s.ExecutionState = "deferred"
		if s.RetryAt != nil {
			s.ExecutionState = "retry_wait"
		}
		s.ExecutionReason = reason
		if reason == "account_busy" {
			s.BusyDeferrals = min(1000, s.BusyDeferrals+1)
		} else {
			s.BusyDeferrals = 0
		}
		s.NextRunAt = &next
		return nil
	})
	return err
}

// StartIQCheck rechecks account health and budgets under the same global lock used by claims.
// A committed attempt is conservatively counted even if a process dies before transport delivery.
func (r *accountRepository) StartIQCheck(ctx context.Context, c service.IQCheckClaim, now time.Time, groups map[string]service.IQQuotaPolicy) (bool, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	db := tx.Client()
	if _, err = db.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421021)"); err != nil {
		return false, err
	}
	a, err := db.Account.Query().Where(dbaccount.IDEQ(c.AccountID)).ForUpdate().Only(ctx)
	if err != nil {
		return false, err
	}
	s := a.IqCheck.WithSettings(nil)
	if s.LeaseToken != c.Token || s.StartedAt != nil {
		return false, nil
	}
	if !s.Enabled || s.Revision != c.Revision || s.LeaseUntil == nil || !s.LeaseUntil.After(now) {
		// This worker owns an unsent reservation that the current configuration invalidated.
		s.LeaseToken = ""
		s.LeaseUntil = nil
		if s.Enabled {
			queueIQ(&s, now)
		}
		if _, err = db.Account.UpdateOneID(c.AccountID).SetIqCheck(s).Save(ctx); err != nil {
			return false, err
		}
		if err = enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &c.AccountID, nil, nil); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		r.syncSchedulerAccountSnapshot(ctx, c.AccountID)
		return false, nil
	}
	if s.RetryAt != nil && (s.AttemptCount != 1 || s.RoundDeadline == nil || !s.RoundDeadline.After(now)) {
		if err := closeIQRetry(ctx, db, c.AccountID, &s, now, "retry_deadline_exceeded"); err != nil {
			return false, err
		}
		if _, err = db.Account.UpdateOneID(c.AccountID).SetIqCheck(s).Save(ctx); err != nil {
			return false, err
		}
		if err = enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &c.AccountID, nil, nil); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		r.syncSchedulerAccountSnapshot(ctx, c.AccountID)
		return false, nil
	}
	next, reason := s.Eligibility(now)
	deferUntil := func(until time.Time, why string) {
		if reason == "" || until.After(next) {
			next, reason = until, why
		}
	}
	if s.QuotaGroup != c.QuotaGroup {
		deferUntil(now.Add(time.Minute), "configuration_changed")
	}
	if a.Status != service.StatusActive || !a.Schedulable {
		deferUntil(now.Add(time.Minute), "account_disabled")
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now) {
		deferUntil(now.Add(time.Minute), "account_expired")
	}
	// Business traffic can update cooldowns after the worker's initial health check.
	// Recheck the locked row before reserving budget or recording an attempt.
	for _, until := range []*time.Time{a.OverloadUntil, a.RateLimitResetAt, a.TempUnschedulableUntil} {
		if until != nil && until.After(now) {
			deferUntil(*until, "account_cooldown")
		}
	}
	quotaAccount := &service.Account{Type: a.Type, Extra: a.Extra}
	if quotaAccount.IsAPIKeyOrBedrock() && quotaAccount.IsQuotaExceeded() {
		deferUntil(now.Add(time.Minute), "account_quota")
	}
	// Shared quota identity is explicit; an unknown group fails closed.
	if s.QuotaGroup != "" {
		policy, ok := groups[s.QuotaGroup]
		if !ok {
			deferUntil(now.Add(time.Minute), "quota_group_unconfigured")
		} else {
			if _, err = db.ExecContext(ctx, `INSERT INTO iq_check_quota_groups(id,budget_day,used) VALUES($1,$2,0) ON CONFLICT DO NOTHING`, s.QuotaGroup, now.UTC().Format("2006-01-02")); err != nil {
				return false, err
			}
			rows, e := db.QueryContext(ctx, `SELECT budget_day,used,not_before,next_start_at,paused_reason FROM iq_check_quota_groups WHERE id=$1 FOR UPDATE`, s.QuotaGroup)
			if e != nil {
				return false, e
			}
			var day string
			var used int
			var cooldown, rate *time.Time
			var paused string
			if rows.Next() {
				err = rows.Scan(&day, &used, &cooldown, &rate, &paused)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
			if err != nil {
				return false, err
			}
			if day != now.UTC().Format("2006-01-02") {
				used = 0
			}
			if paused != "" {
				deferUntil(now.Add(time.Minute), "group_paused")
			}
			if used >= policy.DailyLimit {
				t := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
				if t.After(next) {
					next = t
					reason = "group_daily_budget"
				}
			}
			for _, t := range []*time.Time{cooldown, rate} {
				if t != nil && t.After(next) {
					next = *t
					reason = "group_cooldown"
				}
			}
			if reason == "" {
				rateAt := now.Add(time.Duration(policy.MinIntervalSeconds) * time.Second)
				if _, err = db.ExecContext(ctx, `UPDATE iq_check_quota_groups SET budget_day=$2,used=$3,next_start_at=$4 WHERE id=$1`, s.QuotaGroup, now.UTC().Format("2006-01-02"), used+1, rateAt); err != nil {
					return false, err
				}
			}
		}
	}
	if reason != "" && s.RetryAt != nil && s.RoundDeadline != nil && !next.Before(*s.RoundDeadline) {
		if err := closeIQRetry(ctx, db, c.AccountID, &s, now, reason); err != nil {
			return false, err
		}
		if s.NextRunAt == nil || next.After(*s.NextRunAt) {
			s.NextRunAt = &next
		}
		if _, err = db.Account.UpdateOneID(c.AccountID).SetIqCheck(s).Save(ctx); err != nil {
			return false, err
		}
		if err = enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &c.AccountID, nil, nil); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		r.syncSchedulerAccountSnapshot(ctx, c.AccountID)
		return false, nil
	}
	if reason != "" {
		if s.RetryAt != nil {
			s.ExecutionState = "retry_wait"
		}
		s.BusyDeferrals = 0
		s.LeaseToken = ""
		s.LeaseUntil = nil
		s.ExecutionState = "deferred"
		if s.RetryAt != nil {
			s.ExecutionState = "retry_wait"
		}
		s.ExecutionReason = reason
		s.NextRunAt = &next
	} else {
		s.BusyDeferrals = 0
		day := now.UTC().Format("2006-01-02")
		if s.BudgetDay != day {
			s.BudgetDay = day
			s.BudgetUsed = 0
		}
		if err = recordIQMetric(ctx, db, c.AccountID, now, "sent", "", 0); err != nil {
			return false, err
		}
		if s.NextRunAt != nil {
			wait := max(int64(0), now.Sub(*s.NextRunAt).Milliseconds())
			for _, bound := range []int64{5000, 15000, 30000, 60000} {
				if wait <= bound {
					if err = recordIQMetric(ctx, db, c.AccountID, now, "queue_le", fmt.Sprint(bound), wait); err != nil {
						return false, err
					}
				}
			}
			if err = recordIQMetric(ctx, db, c.AccountID, now, "queue", "", wait); err != nil {
				return false, err
			}
		}
		s.BudgetUsed++
		if s.RoundID == "" {
			s.RoundStartedAt = &now
			s.RoundID = c.Token
			s.AttemptCount = 0
			deadline := now.Add(time.Duration(2*s.TimeoutSeconds+30) * time.Second)
			s.RoundDeadline = &deadline
		}
		s.AttemptCount++
		s.StartedAt = &now
		s.LastAttemptAt = &now
		s.ExecutionState = "running"
		s.ExecutionReason = ""
		until := now.Add(time.Duration(s.TimeoutSeconds+60) * time.Second)
		s.LeaseUntil = &until
		if s.AttemptCount == 1 {
			if _, err = db.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,started_at,prompt_version,grader_version,model,effort,output_mode,protocol,config_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, c.AccountID, c.Token, now, iqcheck.PromptVersion, iqcheck.GraderVersion, s.Model, s.ReasoningEffort, s.OutputMode, c.Protocol, c.Revision); err != nil {
				return false, err
			}
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO account_iq_check_attempts(result_id,attempt_no,lease_token,started_at) SELECT id,$2,$3,$4 FROM account_iq_check_results WHERE lease_token=$1`, s.RoundID, s.AttemptCount, c.Token, now); err != nil {
			return false, err
		}
		if _, err = db.ExecContext(ctx, `DELETE FROM account_iq_check_results WHERE account_id=$1 AND id NOT IN (SELECT id FROM account_iq_check_results WHERE account_id=$1 ORDER BY id DESC LIMIT 3)`, c.AccountID); err != nil {
			return false, err
		}
	}
	if reason != "" {
		if err = recordIQMetric(ctx, db, c.AccountID, now, "deferred", reason, 0); err != nil {
			return false, err
		}
	}
	if _, err = db.Account.UpdateOneID(c.AccountID).SetIqCheck(s).Save(ctx); err != nil {
		return false, err
	}
	if err = enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &c.AccountID, nil, nil); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	r.syncSchedulerAccountSnapshot(ctx, c.AccountID)
	return reason == "", nil
}

func queueIQ(s *domain.IQCheck, now time.Time) {
	if s.LeaseUntil != nil && s.LeaseUntil.After(now) {
		return
	}
	if s.RetryAt != nil {
		return
	}
	if s.TaskID == "" {
		s.TaskID = uuid.NewString()
	}
	next, reason := s.Eligibility(now)
	s.NextRunAt = &next
	s.ExecutionState = "pending"
	s.ExecutionReason = ""
	if reason != "" {
		s.ExecutionState = "deferred"
		s.ExecutionReason = reason
	}
}
