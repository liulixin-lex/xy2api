package mcp

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (r *runtimeReader) health(ctx context.Context, tx *sql.Tx, q *readerQuery, accounts []map[string]any) error {
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		id, ok := a["account_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		q.source("usage_logs", "ops_error_logs.upstream_errors", "redis.upstream_balance_snapshot")
		q.result.Items = accounts
		return nil
	}
	now := q.result.GeneratedAt
	observations, err := recentHealthObservations(ctx, tx, ids, now)
	if err != nil {
		return err
	}
	if r.balances == nil {
		return ErrUnavailable
	}
	stored, err := r.balances.Load(ctx, ids)
	if err != nil {
		return err
	}
	if stored == nil {
		return ErrUnavailable
	}
	for _, a := range accounts {
		id, ok := a["account_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		health := service.AccountRecentHealthFor(observations[id], now)
		a["failure"] = health.Failure
		a["first_output"] = health.FirstOutput
		a["cache"] = health.Cache
		balance := map[string]any{"status": "snapshot_missing", "credential_match": "unverified"}
		if v := stored.Records[id]; v != nil {
			balance["status"] = "stored_snapshot"
			balance["amount_usd"] = v.AmountUSD
			balance["unlimited"] = v.Unlimited
			balance["observed_at"] = v.ObservedAt.UTC()
			balance["stale"] = now.Sub(v.ObservedAt) > 15*time.Minute
		}
		if v := stored.States[id]; v != nil {
			balance["last_attempt_at"] = v.LastAttemptAt.UTC()
			balance["last_http_status"] = v.LastHTTPStatus
			balance["next_due_at"] = v.NextDueAt.UTC()
		}
		a["balance"] = balance
	}
	q.source("usage_logs", "ops_error_logs.upstream_errors", "redis.upstream_balance_snapshot")
	q.result.Items = accounts
	q.result.StateScope = "persisted_recent_health_and_passively_stored_balance; no_probe"
	window := TimeWindow{now.Add(-service.AccountRecentHistoryWindow), now}
	q.result.Window = &window
	q.result.PartialReasons = append(q.result.PartialReasons, "balance_credential_fingerprint_unverified; balance_snapshot_may_be_stale", "failure_max_200_events_1h; first_output_and_cache_max_50_strict_continuations_2h")
	return nil
}

func recentHealthObservations(ctx context.Context, tx *sql.Tx, ids []int64, now time.Time) (map[int64][]service.GroupQualityObservation, error) {
	result := make(map[int64][]service.GroupQualityObservation, len(ids))
	// LAG includes every account and unsuccessful placeholder in the bounded
	// session history. Filtering accounts before LAG would invent continuations.
	rows, err := tx.QueryContext(ctx, `WITH history AS MATERIALIZED (
 SELECT id,account_id,api_key_id,request_id,created_at,stream,model,
 (actual_cost>0 OR output_tokens>0) AS is_success,
 COALESCE(NULLIF(end_to_end_first_output_ms,0),first_token_ms,0) AS first_output_ms,
 COALESCE(session_id,'') AS session_id,
 input_tokens::bigint+cache_creation_tokens+cache_read_tokens AS prompt_tokens,cache_read_tokens,
 LAG(account_id) OVER session_order AS previous_account_id,LAG(model) OVER session_order AS previous_model,
 LAG(created_at) OVER session_order AS previous_at,
 LAG(input_tokens::bigint+cache_creation_tokens+cache_read_tokens) OVER session_order AS previous_prompt_tokens
 FROM public.usage_logs WHERE created_at >= $1 AND created_at < $2
 WINDOW session_order AS (PARTITION BY api_key_id,COALESCE(origin_group_id,group_id),session_id ORDER BY created_at,id)
 ),valid AS MATERIALIZED (
 SELECT id,account_id,api_key_id,request_id,created_at,stream,first_output_ms,prompt_tokens,cache_read_tokens,
 COALESCE(session_id<>'' AND previous_account_id=account_id AND previous_model=model AND previous_at IS NOT NULL AND created_at-previous_at<=INTERVAL '5 minutes',FALSE) AS strict_continuation,
 COALESCE(previous_prompt_tokens,0) AS previous_prompt_tokens FROM history WHERE account_id=ANY($3) AND is_success
 ),failure_candidates AS (
 SELECT id,created_at,ROW_NUMBER() OVER(PARTITION BY account_id ORDER BY created_at DESC,id DESC) AS n FROM valid WHERE created_at >= $2::timestamptz-INTERVAL '1 hour'
 ),ttft_candidates AS (
 SELECT id,created_at,ROW_NUMBER() OVER(PARTITION BY account_id ORDER BY created_at DESC,id DESC) AS n FROM valid WHERE strict_continuation AND stream AND first_output_ms>0 AND created_at >= $2::timestamptz-INTERVAL '2 hours'
 ),cache_candidates AS (
 SELECT id,created_at,ROW_NUMBER() OVER(PARTITION BY account_id ORDER BY created_at DESC,id DESC) AS n FROM valid WHERE strict_continuation AND prompt_tokens>=2048 AND previous_prompt_tokens>=2048 AND created_at >= $2::timestamptz-INTERVAL '2 hours'
 ),selected AS (SELECT id,created_at FROM failure_candidates WHERE n<=200 UNION SELECT id,created_at FROM ttft_candidates WHERE n<=50 UNION SELECT id,created_at FROM cache_candidates WHERE n<=50)
 SELECT v.account_id,'key:'||v.api_key_id::text||'|'||COALESCE(NULLIF(v.request_id,''),'row:'||v.id::text),v.created_at,v.stream,v.first_output_ms,v.prompt_tokens,v.cache_read_tokens,v.strict_continuation,v.previous_prompt_tokens
 FROM valid v JOIN selected s ON s.id=v.id AND s.created_at=v.created_at ORDER BY v.account_id,v.created_at,v.id`, now.Add(-service.AccountRecentHistoryWindow), now, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o service.GroupQualityObservation
		if err = rows.Scan(&o.AccountID, &o.RequestID, &o.At, &o.Streaming, &o.FirstOutputMs, &o.PromptTokens, &o.CacheReadTokens, &o.StrictContinuation, &o.PreviousPromptTokens); err != nil {
			_ = rows.Close()
			return nil, err
		}
		o.Success = true
		result[o.AccountID] = append(result[o.AccountID], o)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// Only the classifier's boolean predicate touches message text, inside SQL.
	// No free upstream text or whole event object is selected into Go.
	rows, err = tx.QueryContext(ctx, `WITH projected AS (
 SELECT e.id,e.api_key_id,e.created_at,ordinal,
 CASE WHEN COALESCE(e.client_request_id,'')<>'' THEN 'client:'||e.client_request_id WHEN COALESCE(e.request_id,'')<>'' THEN 'local:'||e.request_id ELSE 'row:'||e.id::text||':'||ordinal::text END AS request_ref,
 `+numericJSON("account_id")+` AS account_id,
 COALESCE(`+numericJSON("at_unix_ms")+`,(EXTRACT(EPOCH FROM e.created_at)*1000)::bigint) AS at_ms,
 `+enumJSON("kind", "http_error", "request_error", "retry_exhausted", "failover", "first_output_timeout", "stream_failed", "stream_error")+` AS kind,
 `+enumJSON("resilience_category", "TRANSIENT", "FIRST_OUTPUT_TIMEOUT", "STALL", "BALANCE", "AUTH", "RATE_LIMIT", "OVERLOAD")+` AS category,
 (COALESCE(ev->>'resilience_category','') IN ('TRANSIENT','FIRST_OUTPUT_TIMEOUT','STALL') OR (COALESCE(ev->>'resilience_category','')='' AND ev->>'kind'='request_error' AND COALESCE(`+numericJSON("upstream_status_code")+`,0)=0 AND POSITION('context canceled' IN LOWER(COALESCE(ev->>'message','')))=0)) AS soft_failure
 FROM public.ops_error_logs e CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.upstream_errors)='array' THEN e.upstream_errors ELSE '[]'::jsonb END) WITH ORDINALITY AS events(ev,ordinal)
 WHERE e.created_at >= $1 AND e.created_at < $2
 ),dedup AS (
 SELECT DISTINCT ON(api_key_id,request_ref,account_id,at_ms) account_id,'key:'||COALESCE(api_key_id::text,'unknown')||'|'||request_ref AS request_ref,to_timestamp(at_ms/1000.0) AS at,kind,category
 FROM projected WHERE account_id=ANY($3) AND soft_failure AND at_ms >= EXTRACT(EPOCH FROM $1::timestamptz)*1000 AND at_ms < EXTRACT(EPOCH FROM $2::timestamptz)*1000
 ORDER BY api_key_id,request_ref,account_id,at_ms,id,ordinal
 ),ranked AS (SELECT account_id,request_ref,at,kind,category,ROW_NUMBER() OVER(PARTITION BY account_id ORDER BY at DESC,request_ref) AS n FROM dedup)
 SELECT account_id,request_ref,at,kind,category FROM ranked WHERE n<=200 ORDER BY account_id,at`, now.Add(-service.AccountRecentFailureWindow), now, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var o service.GroupQualityObservation
		if err = rows.Scan(&o.AccountID, &o.RequestID, &o.At, &o.Kind, &o.Category); err != nil {
			return nil, err
		}
		o.SoftFailure = true
		o.Failed = true
		result[o.AccountID] = append(result[o.AccountID], o)
	}
	return result, rows.Err()
}
