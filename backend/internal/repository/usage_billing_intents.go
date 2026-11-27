package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/liulixin-lex/xy2api/internal/service"
)

func enableTierBillingProtocol(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "SET LOCAL xy2api.billing_worker_protocol = '2'")
	return err
}

func (r *usageBillingRepository) withTierBillingProtocol(ctx context.Context, run func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := enableTierBillingProtocol(ctx, tx); err != nil {
		return err
	}
	if err := run(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// prepareBillingIntent commits separately from monetary effects. If the money
// transaction rolls back, the exact original price/usage remains recoverable.
func (r *usageBillingRepository) prepareBillingIntent(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingCommand, error) {
	cmd.Usage = service.BillingUsageSnapshot(cmd.Usage)
	payload, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("encode billing intent: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = enableTierBillingProtocol(ctx, tx); err != nil {
		return nil, err
	}
	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO usage_billing_intents (request_id, api_key_id, user_id, request_fingerprint, command)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
	`, cmd.RequestID, cmd.APIKeyID, cmd.UserID, cmd.RequestFingerprint, string(payload))
	if err != nil {
		return nil, fmt.Errorf("persist billing intent: %w", err)
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n > 0 && cmd.ResolveCompensation != nil {
		frozen := *cmd
		frozen.Usage = service.BillingUsageSnapshot(cmd.Usage)
		cmd.ResolveCompensation(&frozen)
		if err = frozen.Validate(); err != nil {
			return nil, err
		}
		payload, err = json.Marshal(&frozen)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE usage_billing_intents SET command=$3::jsonb WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID, string(payload)); err != nil {
			return nil, err
		}
	}
	var fingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, command FROM usage_billing_intents
		WHERE request_id = $1 AND api_key_id = $2`, cmd.RequestID, cmd.APIKeyID).Scan(&fingerprint, &payload); err != nil {
		return nil, err
	}
	if fingerprint != cmd.RequestFingerprint {
		return nil, service.ErrUsageBillingRequestConflict
	}
	var original service.UsageBillingCommand
	if err := json.Unmarshal(payload, &original); err != nil {
		return nil, err
	}
	if err = original.Validate(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &original, nil
}

func lockBillingIntent(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	var fingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT request_fingerprint FROM usage_billing_intents
		WHERE request_id = $1 AND api_key_id = $2 FOR UPDATE`, cmd.RequestID, cmd.APIKeyID).Scan(&fingerprint); err != nil {
		return err
	}
	if fingerprint != cmd.RequestFingerprint {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

func persistSettledBillingUsage(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, applied bool) error {
	inserted, err := (&usageLogRepository{}).createSingle(ctx, tx, cmd.Usage)
	if err != nil {
		return err
	}
	// A pre-existing usage row without a matching committed dedup entry is
	// ambiguous historical data. Do not silently charge it or overwrite it.
	if applied && !inserted {
		return service.ErrUsageBillingRequestConflict
	}
	if !inserted {
		var matches bool
		err := tx.QueryRowContext(ctx, `SELECT user_id = $3 AND account_id = $4 AND actual_cost = $5
			FROM usage_logs WHERE request_id = $1 AND api_key_id = $2`,
			cmd.RequestID, cmd.APIKeyID, cmd.UserID, cmd.AccountID, cmd.Usage.ActualCost).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return service.ErrUsageBillingRequestConflict
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE usage_billing_intents SET state = 'settled',
		last_error_code = NULL, updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2`, cmd.RequestID, cmd.APIKeyID)
	return err
}

// Only bounded error classifications are stored; SQL errors can contain data.
func (r *usageBillingRepository) recordBillingIntentFailure(ctx context.Context, cmd *service.UsageBillingCommand, cause error) {
	state, code := "pending", "settlement_unavailable"
	switch {
	case errors.Is(cause, service.ErrUsageBillingRequestConflict):
		state, code = "review", "fingerprint_or_usage_conflict"
	case errors.Is(cause, service.ErrUserNotFound), errors.Is(cause, service.ErrSubscriptionNotFound), errors.Is(cause, service.ErrAccountNotFound):
		state, code = "review", "billing_subject_missing"
	case errors.Is(cause, service.ErrUsageBillingInvalidAmount):
		state, code = "review", "invalid_amount"
	}
	_ = r.withTierBillingProtocol(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE usage_billing_intents SET state = $3, last_error_code = $4,
			next_attempt_at = NOW() + INTERVAL '10 seconds', updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2 AND state = 'pending'`, cmd.RequestID, cmd.APIKeyID, state, code)
		return err
	})
}

func (r *usageBillingRepository) ClaimBillingRecovery(ctx context.Context, limit int) ([]service.UsageBillingCommand, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var payloads [][]byte
	err := r.withTierBillingProtocol(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `WITH candidates AS (
			SELECT request_id, api_key_id FROM usage_billing_intents
			WHERE (state = 'pending' OR (state = 'settled' AND cache_pending)) AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED
		) UPDATE usage_billing_intents i SET attempts = attempts + 1,
			next_attempt_at = NOW() + INTERVAL '60 seconds', updated_at = NOW()
		FROM candidates c WHERE i.request_id = c.request_id AND i.api_key_id = c.api_key_id
		RETURNING i.command`, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			payloads = append(payloads, raw)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	commands := make([]service.UsageBillingCommand, 0, len(payloads))
	for _, raw := range payloads {
		var cmd service.UsageBillingCommand
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func (r *usageBillingRepository) CompleteBillingCacheInvalidation(ctx context.Context, cmd *service.UsageBillingCommand) error {
	return r.withTierBillingProtocol(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE usage_billing_intents SET cache_pending = FALSE, updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2 AND state = 'settled'`, cmd.RequestID, cmd.APIKeyID)
		return err
	})
}

func (r *usageBillingRepository) BillingRecoveryHealth(ctx context.Context) (pending, review int64, oldestSeconds float64, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE state = 'pending'),
		COUNT(*) FILTER (WHERE state = 'review'),
		COALESCE(EXTRACT(EPOCH FROM NOW() - MIN(created_at) FILTER (WHERE state = 'pending')), 0)
		FROM usage_billing_intents WHERE state <> 'settled'`).Scan(&pending, &review, &oldestSeconds)
	return
}
