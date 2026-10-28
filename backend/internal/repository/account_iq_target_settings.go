package repository

import (
	"context"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/domain"
)

// ListIQTargetSettings reads the persisted target configuration for account
// duplication and export. Runtime verdict and lease state are intentionally
// excluded; callers decide whether copied targets start enabled.
func (r *accountRepository) ListIQTargetSettings(ctx context.Context, ids []int64) (map[int64][]domain.IQTargetSettings, error) {
	out := make(map[int64][]domain.IQTargetSettings)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT account_id,model,enabled,reasoning_effort,interval_minutes,timeout_seconds FROM account_iq_targets WHERE account_id = ANY($1) ORDER BY account_id,model`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var accountID int64
		var settings domain.IQTargetSettings
		var enabled bool
		var effort string
		var intervalMinutes, timeoutSeconds int
		if err := rows.Scan(&accountID, &settings.Model, &enabled, &effort, &intervalMinutes, &timeoutSeconds); err != nil {
			return nil, err
		}
		settings.Enabled = &enabled
		settings.ReasoningEffort = &effort
		settings.IntervalMinutes = &intervalMinutes
		settings.TimeoutSeconds = &timeoutSeconds
		out[accountID] = append(out[accountID], settings)
	}
	return out, rows.Err()
}
