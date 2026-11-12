package repository

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Lock before comparing so concurrent credential updates cannot invalidate the
// Go decision before SQL applies the seed. Callers keep the transaction open.
func anthropicCompatRotationIDs(ctx context.Context, exec sqlExecutor, ids []int64, credentials, extra map[string]any) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, `SELECT id, credentials, extra FROM accounts
        WHERE id = ANY($1) AND deleted_at IS NULL AND platform = 'anthropic' AND type = 'apikey'
        ORDER BY id FOR NO KEY UPDATE`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	rotated := make([]int64, 0)
	for rows.Next() {
		var id int64
		var rawCredentials, rawExtra []byte
		if err := rows.Scan(&id, &rawCredentials, &rawExtra); err != nil {
			return nil, err
		}
		previous := service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey}
		if len(rawCredentials) > 0 {
			if err := json.Unmarshal(rawCredentials, &previous.Credentials); err != nil {
				return nil, err
			}
		}
		if len(rawExtra) > 0 {
			if err := json.Unmarshal(rawExtra, &previous.Extra); err != nil {
				return nil, err
			}
		}
		next := previous
		next.Credentials, next.Extra = maps.Clone(previous.Credentials), maps.Clone(previous.Extra)
		if next.Credentials == nil {
			next.Credentials = make(map[string]any)
		}
		if next.Extra == nil {
			next.Extra = make(map[string]any)
		}
		maps.Copy(next.Credentials, credentials)
		maps.Copy(next.Extra, extra)
		oldKey, oldURL, oldBearer := service.NormalizeAnthropicCompatCredentials(&previous)
		newKey, newURL, newBearer := service.NormalizeAnthropicCompatCredentials(&next)
		if oldKey != newKey || oldURL != newURL || oldBearer != newBearer {
			rotated = append(rotated, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rotated, rows.Close()
}
