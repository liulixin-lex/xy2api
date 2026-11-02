package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type groupQualityGateRepository struct {
	db *sql.DB
}

// NewGroupQualityGateRepository creates the read-only metric and versioned-setting repository.
func NewGroupQualityGateRepository(_ *ent.Client, db *sql.DB) service.GroupQualityGateRepository {
	return &groupQualityGateRepository{db: db}
}

func (r *groupQualityGateRepository) LoadPolicies(ctx context.Context) (service.GroupQualityGatePolicies, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyGroupQualityGatePolicies).Scan(&raw)
	if err == sql.ErrNoRows {
		return service.GroupQualityGatePolicies{Groups: map[string]service.GroupQualityGatePolicy{}}, nil
	}
	if err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	var out service.GroupQualityGatePolicies
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, fmt.Errorf("decode group quality gate policies: %w", err)
	}
	if out.Groups == nil {
		out.Groups = map[string]service.GroupQualityGatePolicy{}
	}
	return out, nil
}

func (r *groupQualityGateRepository) CompareAndSwapPolicies(ctx context.Context, expected int, next service.GroupQualityGatePolicies) (service.GroupQualityGatePolicies, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,'{"version":0,"groups":{}}',NOW()) ON CONFLICT(key) DO NOTHING`, service.SettingKeyGroupQualityGatePolicies); err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1 FOR UPDATE`, service.SettingKeyGroupQualityGatePolicies).Scan(&raw)
	current := service.GroupQualityGatePolicies{Groups: map[string]service.GroupQualityGatePolicy{}}
	if err == sql.ErrNoRows {
		if expected != 0 {
			return service.GroupQualityGatePolicies{}, service.ErrGroupQualityGateVersionConflict
		}
	} else if err != nil {
		return service.GroupQualityGatePolicies{}, err
	} else if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	if current.Version != expected {
		return current, service.ErrGroupQualityGateVersionConflict
	}
	b, err := json.Marshal(next)
	if err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`, service.SettingKeyGroupQualityGatePolicies, string(b)); err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	return next, nil
}

// LoadMembers loads only fields needed by quality and the shared resilience/profit
// checks, in one query. It does not hydrate proxies or group objects.
func (r *groupQualityGateRepository) LoadMembers(ctx context.Context, groupIDs []int64) (map[int64][]service.Account, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, err
	}
	return loadGroupQualityMembers(ctx, tx, groupIDs)
}

func loadGroupQualityMembers(ctx context.Context, tx *sql.Tx, groupIDs []int64) (map[int64][]service.Account, error) {
	out := make(map[int64][]service.Account, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT ag.group_id, a.id, a.platform, a.type, a.status, a.schedulable,
		       a.rate_multiplier, a.expires_at, a.auto_pause_on_expired,
		       a.rate_limit_reset_at, a.overload_until, a.temp_unschedulable_until,
		       COALESCE(a.temp_unschedulable_reason,''), COALESCE(a.extra,'{}'::jsonb)
		FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
		WHERE ag.group_id = ANY($1) AND a.deleted_at IS NULL
		ORDER BY ag.group_id, a.id`, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var groupID int64
		var a service.Account
		var extra []byte
		if err := rows.Scan(&groupID, &a.ID, &a.Platform, &a.Type, &a.Status, &a.Schedulable,
			&a.RateMultiplier, &a.ExpiresAt, &a.AutoPauseOnExpired, &a.RateLimitResetAt,
			&a.OverloadUntil, &a.TempUnschedulableUntil, &a.TempUnschedulableReason, &extra); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, fmt.Errorf("decode quality member extra: %w", err)
		}
		out[groupID] = append(out[groupID], a)
	}
	return out, rows.Err()
}

func (r *groupQualityGateRepository) LoadObservations(ctx context.Context, accountIDs []int64, from, to time.Time) ([]service.GroupQualityObservation, time.Time, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, time.Time{}, err
	}
	return loadGroupQualityObservations(ctx, tx, accountIDs, from, to)
}

func loadGroupQualityObservations(ctx context.Context, tx *sql.Tx, accountIDs []int64, from, to time.Time) ([]service.GroupQualityObservation, time.Time, error) {
	if len(accountIDs) == 0 {
		return nil, to.UTC(), nil
	}
	out := make([]service.GroupQualityObservation, 0)
	rows, err := tx.QueryContext(ctx, `
		WITH history AS (
			SELECT account_id, COALESCE(request_id, '') AS request_id, created_at, stream, COALESCE(NULLIF(end_to_end_first_output_ms,0), first_token_ms, 0) AS first_output_ms,
		       COALESCE(session_id,'') AS session_id,
		       COALESCE(input_tokens, 0) + COALESCE(cache_read_tokens, 0) + COALESCE(cache_creation_tokens, 0) AS prompt_tokens,
		       COALESCE(cache_read_tokens, 0) AS cache_read_tokens,
		       COALESCE(cache_creation_tokens, 0) AS cache_creation_tokens,
		       LAG(created_at) OVER (PARTITION BY account_id, session_id ORDER BY created_at) AS previous_at
			FROM usage_logs WHERE account_id = ANY($1) AND created_at >= $2 AND created_at < $3
		)
		SELECT account_id, request_id, created_at, stream, first_output_ms, session_id,
		       prompt_tokens, cache_read_tokens, cache_creation_tokens, TRUE,
		       session_id <> '' AND previous_at IS NOT NULL AND created_at - previous_at <= INTERVAL '5 minutes'
		FROM history`, pq.Array(accountIDs), from.UTC(), to.UTC())
	if err != nil {
		return nil, time.Time{}, err
	}
	for rows.Next() {
		var o service.GroupQualityObservation
		if err := rows.Scan(&o.AccountID, &o.RequestID, &o.At, &o.Streaming, &o.FirstOutputMs, &o.SessionID, &o.PromptTokens, &o.CacheReadTokens, &o.CacheCreationTokens, &o.Success, &o.Warm); err != nil {
			_ = rows.Close()
			return nil, time.Time{}, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, time.Time{}, err
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, `
		SELECT DISTINCT ON (COALESCE(e.request_id, ''), (ev->>'account_id')::bigint, COALESCE((ev->>'at_unix_ms')::bigint, (EXTRACT(EPOCH FROM e.created_at)*1000)::bigint))
		       COALESCE(e.request_id, ''), COALESCE(e.client_request_id, ''), (ev->>'account_id')::bigint,
		       to_timestamp(COALESCE((ev->>'at_unix_ms')::bigint, (EXTRACT(EPOCH FROM e.created_at)*1000)::bigint)/1000.0),
		       COALESCE(ev->>'kind',''), CASE WHEN jsonb_typeof(ev->'upstream_status_code')='number' THEN (ev->>'upstream_status_code')::int ELSE 0 END,
		       COALESCE(ev->>'resilience_category',''), COALESCE(ev->>'message','')
		FROM ops_error_logs e CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.upstream_errors)='array' THEN e.upstream_errors ELSE '[]'::jsonb END) ev
		WHERE e.created_at >= $2 AND e.created_at < $3 AND e.upstream_errors IS NOT NULL
		  AND jsonb_typeof(ev->'account_id')='number' AND (ev->>'account_id')::bigint = ANY($1)
		ORDER BY COALESCE(e.request_id, ''), (ev->>'account_id')::bigint, COALESCE((ev->>'at_unix_ms')::bigint, (EXTRACT(EPOCH FROM e.created_at)*1000)::bigint), e.id`, pq.Array(accountIDs), from.UTC(), to.UTC())
	if err != nil {
		return nil, time.Time{}, err
	}
	for rows.Next() {
		var o service.GroupQualityObservation
		var clientRequestID string
		if err := rows.Scan(&o.RequestID, &clientRequestID, &o.AccountID, &o.At, &o.Kind, &o.StatusCode, &o.Category, &o.Message); err != nil {
			_ = rows.Close()
			return nil, time.Time{}, err
		}
		// Match the usage_logs request ID of the same request.
		o.RequestID = service.GroupQualityErrorRequestID(clientRequestID, o.RequestID)
		o.SoftFailure = service.IsGroupQualitySoftFailure(o.Kind, o.StatusCode, o.Category, o.Message)
		if o.SoftFailure {
			out = append(out, o)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, time.Time{}, err
	}
	_ = rows.Close()
	var observedAt time.Time
	// A fresh error log must not hide stalled usage writes. Freshness is global,
	// so an idle member does not make an otherwise healthy stream stale.
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_at), $1) FROM usage_logs WHERE created_at < $1`, to.UTC()).Scan(&observedAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	return out, observedAt, nil
}
