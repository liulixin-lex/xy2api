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

func (r *groupQualityGateRepository) LoadObservations(ctx context.Context, accountIDs []int64, from, to time.Time) ([]service.GroupQualityObservation, time.Time, error) {
	if len(accountIDs) == 0 {
		return nil, time.Now().UTC(), nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, time.Time{}, err
	}
	out := make([]service.GroupQualityObservation, 0)
	rows, err := tx.QueryContext(ctx, `
		WITH history AS (
			SELECT account_id, COALESCE(request_id, '') AS request_id, created_at, stream, COALESCE(first_token_ms,0) AS first_token_ms,
		       COALESCE(session_id,'') AS session_id,
		       COALESCE(input_tokens, 0) + COALESCE(cache_read_tokens, 0) + COALESCE(cache_creation_tokens, 0) AS prompt_tokens,
		       COALESCE(cache_read_tokens, 0) AS cache_read_tokens,
		       COALESCE(cache_creation_tokens, 0) AS cache_creation_tokens,
		       LAG(created_at) OVER (PARTITION BY account_id, session_id ORDER BY created_at) AS previous_at
			FROM usage_logs WHERE account_id = ANY($1) AND created_at >= $2 - INTERVAL '5 minutes' AND created_at < $3
		)
		SELECT account_id, request_id, created_at, stream, first_token_ms, session_id,
		       prompt_tokens, cache_read_tokens, cache_creation_tokens, TRUE,
		       session_id <> '' AND previous_at IS NOT NULL AND created_at - previous_at <= INTERVAL '5 minutes'
		FROM history`, pq.Array(accountIDs), from.UTC(), to.UTC())
	if err != nil {
		return nil, time.Time{}, err
	}
	for rows.Next() {
		var o service.GroupQualityObservation
		if err := rows.Scan(&o.AccountID, &o.RequestID, &o.At, &o.Streaming, &o.FirstTokenMs, &o.SessionID, &o.PromptTokens, &o.CacheReadTokens, &o.CacheCreationTokens, &o.Success, &o.Warm); err != nil {
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
		       COALESCE(e.request_id, ''), (ev->>'account_id')::bigint,
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
		if err := rows.Scan(&o.RequestID, &o.AccountID, &o.At, &o.Kind, &o.StatusCode, &o.Category, &o.Message); err != nil {
			_ = rows.Close()
			return nil, time.Time{}, err
		}
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
	err = tx.QueryRowContext(ctx, `
		SELECT GREATEST(
			COALESCE((SELECT MAX(created_at) FROM usage_logs WHERE account_id = ANY($1) AND created_at >= $2 AND created_at < $3), 'epoch'::timestamptz),
			COALESCE((SELECT MAX(e.created_at)
				FROM ops_error_logs e
				WHERE e.created_at >= $2 AND e.created_at < $3
				  AND (e.account_id = ANY($1) OR EXISTS (
					SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(e.upstream_errors)='array' THEN e.upstream_errors ELSE '[]'::jsonb END) ev
					WHERE jsonb_typeof(ev->'account_id')='number' AND (ev->>'account_id')::bigint = ANY($1)
				  ))), 'epoch'::timestamptz)
		)`, pq.Array(accountIDs), from.UTC(), to.UTC()).Scan(&observedAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	if observedAt.Equal(time.Unix(0, 0)) && len(out) == 0 {
		// An account with no observations in the queried metric horizon is a
		// valid no-sample case, not a stale sample.
		observedAt = time.Now().UTC()
	}
	return out, observedAt, nil
}
