package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.ResiliencePenaltyStore = (*accountRepository)(nil)
var _ service.BalancePauseStore = (*accountRepository)(nil)

func (r *accountRepository) SetBalancePauseTiered(ctx context.Context, id int64, basePause time.Duration, attemptStart, startedAt time.Time, evidence string) (service.BalancePauseWrite, error) {
	if r == nil || r.client == nil {
		return service.BalancePauseWrite{}, errors.New("account repository transaction client is unavailable")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return service.BalancePauseWrite{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txSQL := sqlExecutorFromEntClient(tx.Client())
	if txSQL == nil {
		return service.BalancePauseWrite{}, errors.New("account repository transaction executor is unavailable")
	}

	var oldUntil sql.NullTime
	var oldReason sql.NullString
	err = scanSingleRow(ctx, txSQL, `
		SELECT temp_unschedulable_until, temp_unschedulable_reason
		FROM accounts
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, []any{id}, &oldUntil, &oldReason)
	if err != nil {
		if err == sql.ErrNoRows {
			return service.BalancePauseWrite{}, service.ErrAccountNotFound
		}
		return service.BalancePauseWrite{}, err
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	} else {
		startedAt = startedAt.UTC()
	}
	if basePause <= 0 {
		basePause = 300 * time.Second
	}
	currentReason := oldReason.String
	previousUntil := time.Time{}
	if oldUntil.Valid {
		previousUntil = oldUntil.Time
	}
	step := resilience.BalancePauseStepForFailure(currentReason, previousUntil, attemptStart, startedAt)
	duration := basePause
	switch step {
	case 2:
		duration *= 3
	case 3:
		duration *= 12
	}
	until := startedAt.Add(duration)
	active := oldUntil.Valid && previousUntil.After(startedAt)
	if active && resilience.IsBalancePause(currentReason) {
		return service.BalancePauseWrite{Until: previousUntil, Reason: currentReason, Step: step}, nil
	}
	if active {
		if resilience.TempUnschedTier(currentReason) == resilience.TierHard && !until.After(previousUntil) {
			return service.BalancePauseWrite{Until: previousUntil, Reason: currentReason, Step: step}, nil
		}
		if resilience.TempUnschedTier(currentReason) == resilience.TierSoft && previousUntil.After(until) {
			until = previousUntil
		}
	}
	reason := resilience.BalancePauseReasonAtStep(step, evidence)
	var savedUntil time.Time
	var savedReason string
	err = scanSingleRow(ctx, txSQL, `
		UPDATE accounts
		SET temp_unschedulable_until = $1,
			temp_unschedulable_reason = $2,
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL
		RETURNING temp_unschedulable_until, temp_unschedulable_reason
	`, []any{until, reason, id}, &savedUntil, &savedReason)
	if err != nil {
		return service.BalancePauseWrite{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.BalancePauseWrite{}, err
	}
	r.afterTempUnschedulableChange(ctx, id, "balance pause")
	return service.BalancePauseWrite{Changed: true, Until: savedUntil, Reason: savedReason, Step: step}, nil
}

// Tiered temp_unschedulable writes of the gateway resilience layer (xy2api,
// review R01/R02). SetTempUnschedulable only ever extends the pause, so a
// longer soft cooldown kept a balance pause from taking effect and a longer
// soft cooldown could replace an active hard pause. These writes decide by
// the penalty tier of the active reason, in one statement.

// SetTempUnschedulableTiered writes a temp_unschedulable penalty. hard tells
// the tier of the new reason; softPrefixes are the reason prefixes of soft
// penalties. Against an active pause:
//   - hard replaces a soft pause of any length; with keepLaterEnd (balance
//     pauses) it keeps the later of the two ends, otherwise its own end, so a
//     short hard pause does not hold a single-account group for the rest of
//     a long soft cooldown;
//   - hard extends a shorter hard pause; a longer hard pause stays;
//   - soft extends a shorter soft pause; a longer soft pause stays;
//   - soft never replaces a hard pause.
//
// It reports whether the row changed.
func (r *accountRepository) SetTempUnschedulableTiered(ctx context.Context, id int64, until time.Time, reason string, hard, keepLaterEnd bool, softPrefixes []string) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET temp_unschedulable_until = CASE
				WHEN temp_unschedulable_until IS NOT NULL AND temp_unschedulable_until > NOW() AND $6::boolean
					THEN GREATEST(temp_unschedulable_until, $1)
				ELSE $1
			END,
			temp_unschedulable_reason = $2,
			updated_at = NOW()
		WHERE id = $3
			AND deleted_at IS NULL
			AND (
				temp_unschedulable_until IS NULL
				OR temp_unschedulable_until <= NOW()
				OR (temp_unschedulable_until < $1 AND ($4::boolean OR EXISTS (
					SELECT 1 FROM unnest($5::text[]) AS p(prefix)
					WHERE starts_with(btrim(COALESCE(temp_unschedulable_reason, '')), p.prefix))))
				OR ($4::boolean AND EXISTS (
					SELECT 1 FROM unnest($5::text[]) AS p(prefix)
					WHERE starts_with(btrim(COALESCE(temp_unschedulable_reason, '')), p.prefix)))
			)
	`, until, reason, id, hard, pq.Array(softPrefixes), keepLaterEnd)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected <= 0 {
		return false, nil
	}
	r.afterTempUnschedulableChange(ctx, id, "tiered temp unschedulable")
	return true, nil
}

// ClearTempUnschedulableIf clears the pause only while it is still the one
// the caller read (same reason and end), so a success cannot lift a pause
// written after the read. It reports whether the row changed.
func (r *accountRepository) ClearTempUnschedulableIf(ctx context.Context, id int64, reason string, until time.Time) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET temp_unschedulable_until = NULL,
			temp_unschedulable_reason = NULL,
			updated_at = NOW()
		WHERE id = $1
			AND deleted_at IS NULL
			AND temp_unschedulable_reason = $2
			AND temp_unschedulable_until = $3
	`, id, reason, until)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected <= 0 {
		return false, nil
	}
	r.afterTempUnschedulableChange(ctx, id, "conditional clear temp unschedulable")
	return true, nil
}

func (r *accountRepository) afterTempUnschedulableChange(ctx context.Context, id int64, what string) {
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue %s failed: account=%d err=%v", what, id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
}
