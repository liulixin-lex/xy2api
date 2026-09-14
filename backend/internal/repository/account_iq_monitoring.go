package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func finishIQSchedule(s *domain.IQCheck, r iqcheck.Result, now time.Time) {
	s.Status = r.Status
	s.Reason = r.Reason
	s.LastRunAt = &now
	s.StartedAt = nil
	s.ExecutionState = "idle"
	s.ExecutionReason = ""
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
		s.ProtocolFailures = min(3, s.ProtocolFailures+1)
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
	if pause || s.ProtocolFailures >= 3 {
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
		if s.LeaseToken != c.Token {
			return nil
		}
		s.LeaseToken = ""
		s.LeaseUntil = nil
		if s.Revision != c.Revision || !s.Enabled {
			return nil
		}
		s.ExecutionState = "deferred"
		s.ExecutionReason = reason
		s.NextRunAt = &next
		return nil
	})
	return err
}

// StartIQCheck rechecks account and shared budgets under the same global lock used by claims.
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
	if !s.Enabled || s.Revision != c.Revision || s.LeaseToken != c.Token || s.StartedAt != nil {
		return false, nil
	}
	next, reason := s.Eligibility(now)
	if s.QuotaGroup != c.QuotaGroup {
		next, reason = now.Add(time.Minute), "configuration_changed"
	}
	if a.Status != service.StatusActive || !a.Schedulable {
		next, reason = now.Add(time.Minute), "account_disabled"
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now) {
		next, reason = now.Add(time.Minute), "account_expired"
	}
	// Shared quota identity is explicit; an unknown group fails closed.
	if s.QuotaGroup != "" {
		policy, ok := groups[s.QuotaGroup]
		if !ok {
			reason = "quota_group_unconfigured"
			next = now.Add(time.Minute)
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
				next, reason = now.Add(time.Minute), "group_paused"
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
	if reason != "" {
		s.LeaseToken = ""
		s.LeaseUntil = nil
		s.ExecutionState = "deferred"
		s.ExecutionReason = reason
		s.NextRunAt = &next
	} else {
		day := now.UTC().Format("2006-01-02")
		if s.BudgetDay != day {
			s.BudgetDay = day
			s.BudgetUsed = 0
		}
		s.BudgetUsed++
		s.StartedAt = &now
		s.LastAttemptAt = &now
		s.ExecutionState = "running"
		s.ExecutionReason = ""
		until := now.Add(time.Duration(s.TimeoutSeconds+60) * time.Second)
		s.LeaseUntil = &until
		if _, err = db.ExecContext(ctx, `INSERT INTO account_iq_check_results(account_id,lease_token,started_at,prompt_version,grader_version,model,effort,output_mode,protocol,config_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, c.AccountID, c.Token, now, iqcheck.PromptVersion, iqcheck.GraderVersion, s.Model, s.ReasoningEffort, s.OutputMode, c.Protocol, c.Revision); err != nil {
			return false, err
		}
		if _, err = db.ExecContext(ctx, `DELETE FROM account_iq_check_results WHERE account_id=$1 AND id NOT IN (SELECT id FROM account_iq_check_results WHERE account_id=$1 ORDER BY id DESC LIMIT 3)`, c.AccountID); err != nil {
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
