package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/timezone"
)

// Preserve settlement-time day/week and rolling 30-day quota windows.
// An absent row or all limits unset means unlimited, so no new row is inserted.
func incrementBillingPlatformQuota(ctx context.Context, tx *sql.Tx, userID int64, platform string, cost float64) error {
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx, `UPDATE user_platform_quotas SET
		daily_usage_usd = CASE WHEN daily_window_start IS NULL OR daily_window_start < $4 THEN $3 ELSE daily_usage_usd + $3 END,
		weekly_usage_usd = CASE WHEN weekly_window_start IS NULL OR weekly_window_start < $5 THEN $3 ELSE weekly_usage_usd + $3 END,
		monthly_usage_usd = CASE WHEN monthly_window_start IS NULL OR monthly_window_start <= $6::timestamptz - INTERVAL '30 days' THEN $3 ELSE monthly_usage_usd + $3 END,
		daily_window_start = GREATEST(daily_window_start, $4), weekly_window_start = GREATEST(weekly_window_start, $5),
		monthly_window_start = CASE WHEN monthly_window_start IS NULL OR monthly_window_start <= $6::timestamptz - INTERVAL '30 days' THEN $6 ELSE monthly_window_start END,
		updated_at = $6
		WHERE user_id = $1 AND platform = $2 AND deleted_at IS NULL
		AND (daily_limit_usd IS NOT NULL OR weekly_limit_usd IS NOT NULL OR monthly_limit_usd IS NOT NULL)`,
		userID, platform, cost, timezone.StartOfDay(now), timezone.StartOfWeek(now), now)
	return err
}
