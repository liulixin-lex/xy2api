package repository

import (
	"context"
	"database/sql"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type imageTaskBillingLookup struct {
	db *sql.DB
}

// NewImageTaskBillingLookup answers billing questions for async image tasks
// from the usage ledger. Both lookups hit the (request_id, api_key_id) unique
// keys of usage_logs and usage_billing_intents.
func NewImageTaskBillingLookup(sqlDB *sql.DB) service.ImageTaskBillingLookup {
	return &imageTaskBillingLookup{db: sqlDB}
}

func (l *imageTaskBillingLookup) ImageTaskBillingStatus(ctx context.Context, requestID string, apiKeyID int64) (string, error) {
	var charged, pending bool
	err := l.db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM usage_logs WHERE request_id = $1 AND api_key_id = $2),
		EXISTS (SELECT 1 FROM usage_billing_intents WHERE request_id = $1 AND api_key_id = $2 AND state <> 'settled')`,
		requestID, apiKeyID).Scan(&charged, &pending)
	if err != nil {
		return "", err
	}
	switch {
	case charged:
		return service.ImageTaskBillingCharged, nil
	case pending:
		// A durable intent exists: the generation produced billable usage and
		// recovery will settle it; the task reports pending until it does.
		return service.ImageTaskBillingPending, nil
	default:
		return service.ImageTaskBillingNotCharged, nil
	}
}
