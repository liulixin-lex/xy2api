package repository

import (
	"context"
	"encoding/json"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func iqLater(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func iqFinishedAt(wait bool, now time.Time) *time.Time {
	if wait {
		return nil
	}
	return &now
}

// A retry that cannot run closes the existing round without inventing a request.
func closeIQRetry(ctx context.Context, db *dbent.Client, id int64, s *domain.IQCheck, now time.Time, reason string) error {
	round := s.RoundID
	latency := int64(0)
	if s.RoundStartedAt != nil {
		latency = max(int64(0), now.Sub(*s.RoundStartedAt).Milliseconds())
	}
	result := iqcheck.Unknown(reason)
	finishIQSchedule(s, result, now)
	if _, err := db.ExecContext(ctx, `UPDATE account_iq_check_results SET finished_at=$2,reason=$3,latency_ms=GREATEST(0,extract(epoch FROM ($2::timestamptz-started_at))*1000)::bigint WHERE lease_token=$1 AND finished_at IS NULL`, round, now, reason); err != nil {
		return err
	}
	return recordIQMetric(ctx, db, id, now, "round", reason, latency)
}

func recordIQMetric(ctx context.Context, db *dbent.Client, id int64, now time.Time, event, reason string, latency int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO account_iq_check_metrics_hourly(account_id,bucket,event,reason,count,latency_ms) VALUES($1,$2,$3,$4,1,$5) ON CONFLICT(account_id,bucket,event,reason) DO UPDATE SET count=account_iq_check_metrics_hourly.count+1,latency_ms=account_iq_check_metrics_hourly.latency_ms+EXCLUDED.latency_ms`, id, now.UTC().Truncate(time.Hour), event, reason, max(0, latency))
	return err
}

func recoverIQRetryDeadlines(ctx context.Context, db *dbent.Client, now time.Time) error {
	rows, err := db.QueryContext(ctx, `SELECT id,iq_check FROM accounts WHERE deleted_at IS NULL AND iq_check->>'retry_at' IS NOT NULL AND (iq_check->>'round_deadline')::timestamptz <= $1 AND COALESCE((iq_check->>'lease_until')::timestamptz,'-infinity') <= $1 FOR UPDATE`, now)
	if err != nil {
		return err
	}
	type entry struct {
		id int64
		s  domain.IQCheck
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var raw []byte
		if err = rows.Scan(&e.id, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &e.s); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err = closeIQRetry(ctx, db, e.id, &e.s, now, "retry_deadline_exceeded"); err != nil {
			return err
		}
		if _, err = db.Account.UpdateOneID(e.id).SetIqCheck(e.s).Save(ctx); err != nil {
			return err
		}
		if err = enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &e.id, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// Hourly diagnostics are aggregate metadata only; answer and reasoning text never enter this table.
func (r *accountRepository) IQCheckMetrics(ctx context.Context, id int64) ([]service.IQCheckMetric, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT bucket,event,reason,count,latency_ms FROM account_iq_check_metrics_hourly WHERE account_id=$1 AND bucket >= $2 ORDER BY bucket,event,reason`, id, time.Now().UTC().AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.IQCheckMetric{}
	for rows.Next() {
		var m service.IQCheckMetric
		if err = rows.Scan(&m.Bucket, &m.Event, &m.Reason, &m.Count, &m.LatencyMS); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Expired sent attempts are terminal and remain charged. Unsent retry reservations
// can be reclaimed using a new lease without duplicating the registered attempt.
func recoverIQExpiredLeases(ctx context.Context, db *dbent.Client, now time.Time) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,iq_check FROM accounts WHERE deleted_at IS NULL AND iq_check->>'lease_token' <> '' AND (iq_check->>'lease_until')::timestamptz <= $1 FOR UPDATE`, now)
	if err != nil {
		return nil, err
	}
	type entry struct {
		id int64
		s  domain.IQCheck
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var raw []byte
		if err = rows.Scan(&e.id, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &e.s); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, e := range entries {
		s := e.s
		token := s.LeaseToken
		wasStarted := s.StartedAt != nil
		if wasStarted {
			elapsed := max(int64(0), now.Sub(*s.StartedAt).Milliseconds())
			if _, err = db.ExecContext(ctx, `UPDATE account_iq_check_attempts SET finished_at=$2,reason='interrupted',latency_ms=$3 WHERE lease_token=$1 AND finished_at IS NULL`, token, now, elapsed); err != nil {
				return nil, err
			}
			if _, err = db.ExecContext(ctx, `UPDATE account_iq_check_results SET status='unknown',finished_at=$2,reason='interrupted',latency_ms=GREATEST(0,extract(epoch FROM($2::timestamptz-started_at))*1000)::bigint WHERE finished_at IS NULL AND (lease_token=$1 OR id=(SELECT result_id FROM account_iq_check_attempts WHERE lease_token=$1))`, token, now); err != nil {
				return nil, err
			}
			if err = recordIQMetric(ctx, db, e.id, now, "attempt", "interrupted", elapsed); err != nil {
				return nil, err
			}
			roundLatency := elapsed
			if s.RoundStartedAt != nil {
				roundLatency = max(int64(0), now.Sub(*s.RoundStartedAt).Milliseconds())
			}
			if err = recordIQMetric(ctx, db, e.id, now, "round", "interrupted", roundLatency); err != nil {
				return nil, err
			}
			// A configuration reset clears RoundID; do not attribute an old attempt to it.
			if s.RoundID != "" {
				finishIQSchedule(&s, iqcheck.Unknown("interrupted"), now)
			}
		}
		s.LeaseToken = ""
		s.LeaseUntil = nil
		s.StartedAt = nil
		if !wasStarted || s.RoundID != "" || s.NextRunAt == nil {
			if s.RetryAt != nil {
				s.ExecutionState = "retry_wait"
			} else if s.Enabled {
				queueIQ(&s, now)
			}
		}
		if _, err = db.Account.UpdateOneID(e.id).SetIqCheck(s).Save(ctx); err != nil {
			return nil, err
		}
		if err = enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &e.id, nil, nil); err != nil {
			return nil, err
		}
		ids = append(ids, e.id)
	}
	return ids, nil
}
