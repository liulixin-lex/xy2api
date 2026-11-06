package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/sjson"
)

var errGroupQualityReplayRowLimit = errors.New("group quality replay row limit exceeded")

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
	raw, err = patchGroupQualityPolicies(raw, current, next)
	if err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	var saved service.GroupQualityGatePolicies
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return saved, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`, service.SettingKeyGroupQualityGatePolicies, raw); err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.GroupQualityGatePolicies{}, err
	}
	return saved, nil
}

// Patch only changed v3 objects and the version in the locked original JSON.
// Re-marshalling the setting would compact even legacy arrays/objects and
// change their bytes, despite preserving their decoded values.
func patchGroupQualityPolicies(raw string, current, next service.GroupQualityGatePolicies) (string, error) {
	if raw == "" {
		raw = `{"version":0,"groups":{}}`
	}
	for id, policy := range next.Groups {
		previous := current.Groups[id]
		if policy.V3 == nil || (previous.V3 != nil && *previous.V3 == *policy.V3) {
			continue
		}
		if groupID, err := strconv.ParseInt(id, 10, 64); err != nil || groupID <= 0 {
			return "", fmt.Errorf("invalid quality policy group ID %q", id)
		}
		value, err := json.Marshal(policy.V3)
		if err != nil {
			return "", err
		}
		raw, err = sjson.SetRaw(raw, "groups.:"+id+".v3", string(value))
		if err != nil {
			return "", err
		}
	}
	return sjson.Set(raw, "version", next.Version)
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

func (r *groupQualityGateRepository) LoadObservationsForReplay(ctx context.Context, accountIDs []int64, from, to time.Time) (service.GroupQualityReplayData, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return service.GroupQualityReplayData{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '20s'`); err != nil {
		return service.GroupQualityReplayData{}, err
	}
	observations, _, err := loadGroupQualityObservationsBounded(ctx, tx, accountIDs, from, to, service.GroupQualityReplayMaxObservationRows)
	if err != nil {
		return service.GroupQualityReplayData{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT date_trunc('minute', created_at), MAX(created_at)
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY 1
		ORDER BY 1
		LIMIT $3`, from.UTC(), to.UTC(), service.GroupQualityReplayMaxUsageWriteRows+1)
	if err != nil {
		return service.GroupQualityReplayData{}, err
	}
	defer func() { _ = rows.Close() }()
	writes := make([]service.GroupQualityUsageWrite, 0)
	for rows.Next() {
		if len(writes) >= service.GroupQualityReplayMaxUsageWriteRows {
			return service.GroupQualityReplayData{}, errGroupQualityReplayRowLimit
		}
		var write service.GroupQualityUsageWrite
		if err := rows.Scan(&write.Minute, &write.LastWrite); err != nil {
			return service.GroupQualityReplayData{}, err
		}
		writes = append(writes, write)
	}
	if err := rows.Err(); err != nil {
		return service.GroupQualityReplayData{}, err
	}
	return service.GroupQualityReplayData{Observations: observations, UsageWrites: writes}, nil
}

func loadGroupQualityObservations(ctx context.Context, tx *sql.Tx, accountIDs []int64, from, to time.Time) ([]service.GroupQualityObservation, time.Time, error) {
	return loadGroupQualityObservationsBounded(ctx, tx, accountIDs, from, to, 0)
}

func loadGroupQualityObservationsBounded(ctx context.Context, tx *sql.Tx, accountIDs []int64, from, to time.Time, maxRows int) ([]service.GroupQualityObservation, time.Time, error) {
	// Read all OpenAI contributors before LAG. The time range is at most six
	// hours and never predates process startup. Reset bounds are applied in the
	// evaluator without hiding intervening accounts from the window functions.
	out := make([]service.GroupQualityObservation, 0)
	rows, err := tx.QueryContext(ctx, `
  WITH source AS MATERIALIZED (
   SELECT u.id, u.account_id, u.api_key_id, COALESCE(u.group_id,0) AS group_id,
          COALESCE(u.request_id,'') AS request_id, u.created_at, u.stream,
          u.attempt_first_output_ms, COALESCE(u.session_id,'') AS session_id,
          u.model, COALESCE(u.reasoning_effort,'') AS reasoning_effort,
          (u.actual_cost > 0 OR u.output_tokens > 0) AS is_success,
          COALESCE(u.input_tokens,0) + COALESCE(u.cache_read_tokens,0) + COALESCE(u.cache_creation_tokens,0) AS prompt_tokens,
          COALESCE(u.cache_read_tokens,0) AS cache_read_tokens,
          COALESCE(u.cache_creation_tokens,0) AS cache_creation_tokens,
          COALESCE(a.extra->>'group_quality_reset_at','') AS reset_at
   FROM usage_logs u JOIN accounts a ON a.id = u.account_id
		WHERE a.platform = 'openai' AND u.created_at >= $1 AND u.created_at < $3
  ), history AS (
   SELECT *, LAG(account_id) OVER session_order AS previous_account_id,
          LAG(model) OVER session_order AS previous_model,
          LAG(created_at) OVER session_order AS previous_at,
          LAG(prompt_tokens) OVER session_order AS previous_prompt_tokens
   FROM source
   WINDOW session_order AS (PARTITION BY api_key_id, group_id, session_id ORDER BY created_at, id)
  )
  SELECT id, account_id, api_key_id, group_id, request_id, created_at, stream,
         attempt_first_output_ms, session_id, model, reasoning_effort,
         prompt_tokens, cache_read_tokens, cache_creation_tokens,
         COALESCE(session_id <> '' AND previous_account_id = account_id AND previous_model = model
          AND created_at - previous_at <= INTERVAL '5 minutes',FALSE),
         COALESCE(previous_prompt_tokens,0), previous_at, reset_at
	FROM history
	WHERE is_success AND created_at >= $2 AND created_at < $3
		ORDER BY created_at DESC, id DESC`, from.Add(-5*time.Minute).UTC(), from.UTC(), to.UTC())
	if err != nil {
		return nil, time.Time{}, err
	}
	resets := map[int64]time.Time{}
	seen := map[int64]bool{}
	ids := append([]int64(nil), accountIDs...)
	for _, id := range ids {
		seen[id] = true
	}
	for rows.Next() {
		var o service.GroupQualityObservation
		var previousAt sql.NullTime
		var resetAt string
		if err := rows.Scan(&o.ID, &o.AccountID, &o.APIKeyID, &o.GroupID, &o.RequestID, &o.At, &o.Streaming,
			&o.AttemptFirstOutputMs, &o.SessionID, &o.Model, &o.ReasoningEffort,
			&o.PromptTokens, &o.CacheReadTokens, &o.CacheCreationTokens,
			&o.StrictContinuation, &o.PreviousPromptTokens, &previousAt, &resetAt); err != nil {
			_ = rows.Close()
			return nil, time.Time{}, err
		}
		o.PreviousAt = previousAt.Time
		o.ResetAt, _ = time.Parse(time.RFC3339Nano, resetAt)
		o.Success = true
		if maxRows > 0 && len(out) >= maxRows {
			return nil, time.Time{}, errGroupQualityReplayRowLimit
		}
		resets[o.AccountID] = o.ResetAt
		if !seen[o.AccountID] {
			ids = append(ids, o.AccountID)
			seen[o.AccountID] = true
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, time.Time{}, err
	}
	if err := rows.Close(); err != nil {
		return nil, time.Time{}, err
	}
	// All failure categories exclude a failed final account from speed samples.
	// Only the legacy soft subset minus first-output cutoffs enters v3 error rate.
	errorObservations, err := loadQualityErrorObservationsBounded(ctx, tx, ids, from, to, true, maxRows-len(out))
	if err != nil {
		return nil, time.Time{}, err
	}
	for _, o := range errorObservations {
		if maxRows > 0 && len(out) >= maxRows {
			return nil, time.Time{}, errGroupQualityReplayRowLimit
		}
		o.ResetAt = resets[o.AccountID]
		out = append(out, o)
	}
	var observedAt time.Time
	// A fresh error log must not hide stalled usage writes. Freshness is global,
	// so an idle member does not make an otherwise healthy stream stale.
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_at), $1) FROM usage_logs WHERE created_at < $1`, to.UTC()).Scan(&observedAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	return out, observedAt, nil
}

func loadGroupQualityErrorObservations(ctx context.Context, tx interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountIDs []int64, from, to time.Time) ([]service.GroupQualityObservation, error) {
	return loadQualityErrorObservationsBounded(ctx, tx, accountIDs, from, to, false, 0)
}

// includeAll is used only by v3 for success attribution. Recent health retains
// its legacy classifier, including first-output timeouts.
func loadQualityErrorObservations(ctx context.Context, tx interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountIDs []int64, from, to time.Time, includeAll bool) ([]service.GroupQualityObservation, error) {
	return loadQualityErrorObservationsBounded(ctx, tx, accountIDs, from, to, includeAll, 0)
}

func loadQualityErrorObservationsBounded(ctx context.Context, tx interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountIDs []int64, from, to time.Time, includeAll bool, maxRows int) ([]service.GroupQualityObservation, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	out := make([]service.GroupQualityObservation, 0)
	identity := "COALESCE(e.request_id, '')"
	if includeAll {
		identity = "CASE WHEN COALESCE(e.client_request_id,'') <> '' THEN 'client:' || e.client_request_id WHEN COALESCE(e.request_id,'') <> '' THEN 'local:' || e.request_id ELSE 'row:' || e.id::text || ':' || ordinal::text END"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT DISTINCT ON (%s, (ev->>'account_id')::bigint, COALESCE((ev->>'at_unix_ms')::bigint, (EXTRACT(EPOCH FROM e.created_at)*1000)::bigint))
		       COALESCE(e.request_id, ''), COALESCE(e.client_request_id, ''), (ev->>'account_id')::bigint,
		       to_timestamp(COALESCE((ev->>'at_unix_ms')::bigint, (EXTRACT(EPOCH FROM e.created_at)*1000)::bigint)/1000.0),
		       COALESCE(ev->>'kind',''), CASE WHEN jsonb_typeof(ev->'upstream_status_code')='number' THEN (ev->>'upstream_status_code')::int ELSE 0 END,
		       COALESCE(ev->>'resilience_category',''), COALESCE(ev->>'message','')
		FROM ops_error_logs e CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.upstream_errors)='array' THEN e.upstream_errors ELSE '[]'::jsonb END) WITH ORDINALITY AS events(ev, ordinal)
		WHERE e.created_at >= $2 AND e.created_at < $3 AND e.upstream_errors IS NOT NULL
		  AND jsonb_typeof(ev->'account_id')='number' AND (ev->>'account_id')::bigint = ANY($1)
		ORDER BY %s, (ev->>'account_id')::bigint, COALESCE((ev->>'at_unix_ms')::bigint, (EXTRACT(EPOCH FROM e.created_at)*1000)::bigint), e.id`, identity, identity), pq.Array(accountIDs), from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if maxRows > 0 && len(out) >= maxRows {
			return nil, errGroupQualityReplayRowLimit
		}
		var o service.GroupQualityObservation
		var clientRequestID string
		if err := rows.Scan(&o.RequestID, &clientRequestID, &o.AccountID, &o.At, &o.Kind, &o.StatusCode, &o.Category, &o.Message); err != nil {
			return nil, err
		}
		o.RequestID = service.GroupQualityErrorRequestID(clientRequestID, o.RequestID)
		o.SoftFailure = service.IsGroupQualitySoftFailure(o.Kind, o.StatusCode, o.Category, o.Message)
		o.Failed = true
		if o.SoftFailure || includeAll {
			out = append(out, o)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
