package repository

import (
	"context"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.ResiliencePenaltyStore = (*accountRepository)(nil)

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
