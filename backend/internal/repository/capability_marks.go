package repository

import (
	"context"
	"encoding/json"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func limitCapabilityMarkUpdates(ctx context.Context, client *dbent.Client, id int64, updates map[string]any) (map[string]any, error) {
	rows, err := client.QueryContext(ctx, "SELECT extra FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	current := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
	}
	return service.LimitCapabilityMarkUpdates(current, updates, id, time.Now()), nil
}
