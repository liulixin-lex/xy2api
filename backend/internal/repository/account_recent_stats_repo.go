package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type accountRecentStatsRepository struct{ sql sqlExecutor }

func NewAccountRecentStatsRepository(db *sql.DB) service.AccountRecentStatsRepository {
	return &accountRecentStatsRepository{sql: db}
}

func (r *accountRecentStatsRepository) Batch(ctx context.Context, ids []int64, asOf time.Time, monitoring bool) ([]service.AccountRecentStatsRow, error) {
	result := make([]service.AccountRecentStatsRow, 0, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := r.sql.QueryContext(ctx, accountRecentStatsMetricsSQL, pq.Array(ids), asOf)
	if err != nil {
		return nil, err
	}
	positions := make(map[int64]int, len(ids))
	for rows.Next() {
		var row service.AccountRecentStatsRow
		if err := rows.Scan(&row.AccountID, &row.TTFTP50MS, &row.TTFTN, &row.CacheRatio, &row.CacheN); err != nil {
			_ = rows.Close()
			return nil, err
		}
		positions[row.AccountID] = len(result)
		result = append(result, row)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if !monitoring {
		return result, nil
	}
	rows, err = r.sql.QueryContext(ctx, accountRecentStatsErrorsSQL, pq.Array(ids), asOf)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, n, bad int64
		if err := rows.Scan(&id, &n, &bad); err != nil {
			return nil, err
		}
		i := positions[id]
		result[i].ErrorN, result[i].ErrorBad = n, bad
	}
	return result, rows.Err()
}

const accountRecentStatsMetricsSQL = `
WITH ids AS (SELECT unnest($1::bigint[]) account_id), u AS (
 SELECT u.account_id,u.created_at,u.first_token_ms,u.cache_read_tokens::bigint r,
  CASE WHEN input_tokens>=0 AND cache_read_tokens>=0 AND cache_creation_tokens>=0
   THEN input_tokens::bigint+cache_read_tokens::bigint+cache_creation_tokens::bigint END t
 FROM usage_logs u JOIN ids USING(account_id)
 WHERE u.created_at >= $2::timestamptz-interval '30 minutes' AND u.created_at < $2
), metrics AS (
 SELECT account_id,
  percentile_cont(.5) WITHIN GROUP(ORDER BY first_token_ms)
   FILTER(WHERE created_at >= $2-interval '10 minutes' AND first_token_ms>=0) ttft_p50_ms,
  count(*) FILTER(WHERE created_at >= $2-interval '10 minutes' AND first_token_ms>=0) ttft_n,
  sum(r) FILTER(WHERE t>0)::numeric/NULLIF(sum(t) FILTER(WHERE t>0),0) cache_ratio,
  count(*) FILTER(WHERE t>0) cache_n
 FROM u GROUP BY account_id
)
SELECT i.account_id,m.ttft_p50_ms,COALESCE(m.ttft_n,0),m.cache_ratio,COALESCE(m.cache_n,0)
FROM ids i LEFT JOIN metrics m USING(account_id) ORDER BY i.account_id`

// Every usage row is a result, so an older row outside this account's latest
// 100 cannot enter the final 100 after merging failures. Events are filtered
// before ranking and retain the reference SQL's attribution and tie order.
const accountRecentStatsErrorsSQL = `
WITH ids AS (SELECT unnest($1::bigint[]) account_id), u AS (
 SELECT i.account_id,u.id,u.created_at FROM ids i CROSS JOIN LATERAL (
  SELECT id,created_at FROM usage_logs
  WHERE account_id=i.account_id AND created_at >= $2::timestamptz-interval '24 hours' AND created_at < $2
  ORDER BY created_at DESC,id DESC LIMIT 100
 ) u
), e AS MATERIALIZED (
 SELECT id,account_id,created_at,upstream_errors,error_type,error_phase,error_owner,
  upstream_status_code,status_code,is_business_limited,recovery_outcome
 FROM ops_error_logs
 WHERE created_at >= $2::timestamptz-interval '24 hours' AND created_at < $2
), je AS MATERIALIZED (
 SELECT e.id,j,ord FROM e CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(upstream_errors)='array' THEN upstream_errors ELSE '[]'::jsonb END
 ) WITH ORDINALITY x(j,ord)
 WHERE j->>'account_id' ~ '^[1-9][0-9]{0,18}$'
  AND (length(j->>'account_id')<19 OR j->>'account_id'<='9223372036854775807')
), ev AS (
 SELECT id,j,ord FROM je UNION ALL
 SELECT e.id,jsonb_build_object('account_id',e.account_id,'kind',e.error_type,
  'stage',e.error_phase,'owner',e.error_owner,'upstream_status_code',
  COALESCE(e.upstream_status_code,CASE WHEN e.error_owner='provider' THEN e.status_code END)),0
 FROM e WHERE e.account_id>0 AND NOT EXISTS(SELECT 1 FROM je WHERE je.id=e.id)
), failures AS (
 SELECT i.account_id,e.created_at,1 source,e.id,v.ord,true bad
 FROM ev v JOIN e USING(id) JOIN ids i ON i.account_id::text=v.j->>'account_id'
 WHERE NOT COALESCE(e.is_business_limited,false)
  AND COALESCE(e.recovery_outcome,'') NOT IN ('client_cancel','budget_exhausted')
  AND COALESCE(v.j->>'owner','provider')<>'client'
  AND COALESCE(v.j->>'resilience_category','') NOT IN ('BALANCE','CLIENT_ERROR','MODEL_UNSUPPORTED','UPSTREAM_REQUEST_RESTRICTION','CLIENT_CANCEL')
  AND COALESCE(v.j->>'kind','') NOT IN ('billing_error','subscription_error','invalid_request_error','model_not_found','not_found_error')
  AND (v.j->>'resilience_category' IN ('TRANSIENT','STALL','CAPACITY_SHED','AUTH','RATE_LIMIT','RATE_LIMIT_RESET','FIRST_OUTPUT_TIMEOUT')
   OR v.j->>'kind'='first_output_timeout' OR v.j->>'stage'='account_auth'
   OR (COALESCE(v.j->>'owner','provider')='provider' AND v.j->>'upstream_status_code' ~ '^(5[0-9][0-9]|401|403|429)$'))
), ranked AS (
 SELECT *,row_number() OVER(PARTITION BY account_id ORDER BY created_at DESC,source DESC,id DESC,ord DESC) rn
 FROM (SELECT account_id,created_at,0 source,id,0::bigint ord,false bad FROM u
  UNION ALL SELECT * FROM failures) results
)
SELECT account_id,count(*),count(*) FILTER(WHERE bad) FROM ranked WHERE rn<=100 GROUP BY account_id`
