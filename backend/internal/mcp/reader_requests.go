package mcp

import (
	"context"
	"database/sql"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"strconv"
	"strings"
)

const errorClassification = `CASE WHEN e.status_code >= 400 OR e.error_type = 'cyber_policy' THEN 'final_failure' WHEN e.status_code BETWEEN 200 AND 399 AND e.error_owner = 'provider' AND e.error_type = 'upstream_error' AND e.error_phase IN ('upstream','account_auth') THEN 'recovered_upstream' ELSE 'unknown' END`
const providerHealthRow = `e.error_phase IN ('upstream','account_auth')`
const errorPhase = `CASE WHEN e.error_phase IN ('request','auth','routing','upstream','network','internal','account_auth') THEN e.error_phase ELSE 'unknown' END`
const errorType = `CASE WHEN e.error_type IN ('authentication_error','authorization_error','permission_error','rate_limit_error','invalid_request_error','not_found_error','request_too_large','timeout_error','upstream_error','api_error','internal_error','overloaded_error','cyber_policy','content_policy_violation','state_rejection','billing_error','insufficient_quota','concurrency_limit','no_available_accounts','model_not_found','service_unavailable','subscription_error','service_unavailable_error','forbidden_error') THEN e.error_type ELSE 'unknown' END`

func (q *readerQuery) searchRequests(ctx context.Context, tx *sql.Tx) error {
	in := q.request.Input
	if in.IncludeRecoveredUpstream {
		if err := q.require(ctx, "GET /ops/upstream-errors"); err != nil {
			return err
		}
	}
	// One storage row is selected once even when both authorized endpoint sets
	// contain it. Usage settlements retain their own distinct storage identity.
	args := []any{q.window.Start, q.window.End}
	where := q.filters("r.", &args)
	if in.Status != "" {
		switch in.Status {
		case "settled_usage", "final_failure", "recovered_upstream", "unknown":
			addEqual(&where, &args, "r.classification", in.Status)
		default:
			return ErrInvalidInput
		}
	}
	errorsWhere := `COALESCE(e.status_code,0)>=400`
	if in.IncludeRecoveredUpstream {
		errorsWhere = `(` + errorsWhere + ` OR ` + providerHealthRow + `)`
	}
	stmt := `WITH combined AS (
 SELECT ul.created_at AS _page_time,ul.id AS _page_id,'usage_logs'::text AS _page_source,
 'usage_logs'::text AS source,ul.id AS usage_row_id,NULL::bigint AS error_row_id,ul.created_at,ul.request_id,NULL::text AS client_request_id,
 COALESCE(NULLIF(g.platform,''),NULLIF(a.platform,''),'unknown') AS platform,ul.model,ul.duration_ms,ul.first_token_ms,
 NULL::integer AS status_code,NULL::text AS phase,NULL::text AS severity,'settled_usage'::text AS classification,'unknown'::text AS client_outcome,
 ul.user_id,ul.api_key_id,ul.account_id,ul.group_id,ul.stream,'storage_row'::text AS evidence,'exact'::text AS relation_confidence
 FROM public.usage_logs ul LEFT JOIN public.groups g ON g.id=ul.group_id LEFT JOIN public.accounts a ON a.id=ul.account_id
 WHERE ul.created_at >= $1 AND ul.created_at < $2
 UNION ALL
 SELECT e.created_at,e.id,'ops_error_logs',
 CASE WHEN COALESCE(e.status_code,0)>=400 THEN 'ops_requests' ELSE 'provider_health' END,NULL::bigint,e.id,e.created_at,e.request_id,e.client_request_id,
 COALESCE(NULLIF(e.platform,''),NULLIF(g.platform,''),NULLIF(a.platform,''),'unknown'),e.model,e.duration_ms,e.time_to_first_token_ms,
 e.status_code,` + errorPhase + `,CASE WHEN e.severity IN ('P0','P1','P2','P3') THEN e.severity ELSE 'unknown' END,` + errorClassification + `,CASE WHEN e.status_code>=400 OR e.error_type='cyber_policy' THEN 'failure' ELSE 'unknown' END,
 e.user_id,e.api_key_id,e.account_id,e.group_id,e.stream,'persisted_client_status_and_phase','exact'
 FROM public.ops_error_logs e LEFT JOIN public.groups g ON g.id=e.group_id LEFT JOIN public.accounts a ON a.id=e.account_id
 WHERE e.created_at >= $1 AND e.created_at < $2 AND ` + errorsWhere + `
 ) SELECT r.* FROM combined r WHERE ` + andWhere(where)
	q.source("usage_logs", "ops_error_logs")
	q.result.PartialReasons = append(q.result.PartialReasons, "settled_usage_rows_do_not_prove_client_success; source_rows_are_not_unique_client_requests")
	if in.IncludeRecoveredUpstream {
		q.result.Coverage = append(q.result.Coverage, SourceCoverage{Source: "provider_health", Available: true, Note: "not_recovered_only; deduplicated_by_created_at_and_id"})
	}
	return q.paged(ctx, tx, stmt, args)
}

func (q *readerQuery) errorDetail(ctx context.Context, tx *sql.Tx, self bool) error {
	in := q.request.Input
	args := []any{q.window.Start, q.window.End}
	where := []string{"e.created_at >= $1", "e.created_at < $2"}
	detail := !self || q.request.Tool == "xy2api.me.get_error_detail"
	if detail {
		if in.ErrorRowID <= 0 {
			return ErrInvalidInput
		}
		addEqual(&where, &args, "e.id", in.ErrorRowID)
	}
	projection := `e.id AS error_row_id,e.created_at,e.request_id,e.client_request_id,COALESCE(NULLIF(e.requested_model,''),e.model,'') AS requested_model,e.status_code,` + errorType + ` AS classification`
	if detail {
		projection += `,e.duration_ms,e.time_to_first_token_ms`
	}
	if self {
		if in.UserID != 0 && in.UserID != q.request.Actor.UserID {
			return ErrInvalidInput
		}
		addEqual(&where, &args, "e.user_id", q.request.Actor.UserID)
		if in.Status != "" {
			status, err := strconv.Atoi(in.Status)
			if err != nil || status < 100 || status > 599 {
				return ErrInvalidInput
			}
			addEqual(&where, &args, "e.status_code", status)
		}
		where = append(where, `(COALESCE(e.status_code,0)>=400 OR e.error_type='cyber_policy')`)
		if in.APIKeyID > 0 {
			if err := q.ownKey(ctx, tx, in.APIKeyID); err != nil {
				return err
			}
			addEqual(&where, &args, "e.api_key_id", in.APIKeyID)
		}
		for _, f := range []struct{ c, v string }{{"request_id", in.RequestID}, {"client_request_id", in.ClientRequestID}, {"COALESCE(NULLIF(e.requested_model,''),e.model)", in.Model}} {
			if f.v != "" {
				column := "e." + f.c
				if strings.HasPrefix(f.c, "COALESCE(") {
					column = f.c
				}
				addEqual(&where, &args, column, f.v)
			}
		}
	} else {
		projection += `,e.platform,e.model,` + errorPhase + ` AS phase,e.user_id,e.api_key_id,e.account_id,e.group_id,e.stream,e.upstream_status_code,` + errorClassification + ` AS outcome,e.is_business_limited,CASE WHEN e.disconnect_stage IN ('before_output','after_output','upload') THEN e.disconnect_stage ELSE NULL END AS disconnect_stage`
	}
	q.source("ops_error_logs")
	stmt := `SELECT ` + projection + `,'ops_error_logs'::text AS source,'storage_row'::text AS evidence,'exact'::text AS relation_confidence,e.created_at AS _page_time,e.id AS _page_id,'ops_error_logs'::text AS _page_source FROM public.ops_error_logs e WHERE ` + andWhere(where)
	if err := q.paged(ctx, tx, stmt, args); err != nil {
		return err
	}
	if detail && len(q.result.Items) == 0 {
		return ErrNotFound
	}
	return nil
}

// numericJSON never casts arbitrary JSON to an integer. Bounds also prevent a
// malformed retained event from overflowing the scalar projection.
func numericJSON(key string) string {
	return `CASE WHEN jsonb_typeof(ev->'` + key + `')='number' THEN CASE WHEN (ev->>'` + key + `')::numeric BETWEEN 0 AND 9223372036854775807 THEN trunc((ev->>'` + key + `')::numeric)::bigint ELSE NULL END ELSE NULL END`
}

func booleanJSON(key string) string {
	return `CASE WHEN jsonb_typeof(ev->'` + key + `')='boolean' THEN (ev->>'` + key + `')::boolean ELSE NULL END`
}

func enumJSON(key string, values ...string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	return `CASE WHEN ev->>'` + key + `' IN (` + strings.Join(quoted, ",") + `) THEN ev->>'` + key + `' ELSE 'unknown' END`
}
func resilienceCategoryJSON() string {
	return enumJSON("resilience_category", string(resilience.CategorySuccess), string(resilience.CategoryBalance), string(resilience.CategoryAuth), string(resilience.CategoryRateLimitReset), string(resilience.CategoryRateLimit), string(resilience.CategoryCapacityShed), string(resilience.CategoryTransient), string(resilience.CategoryFirstOutputTimeout), string(resilience.CategoryStall), string(resilience.CategoryClientCancel), string(resilience.CategoryClientError), string(resilience.CategoryModelUnsupported))
}
func (q *readerQuery) attempts(ctx context.Context, tx *sql.Tx) error {
	id := q.request.Input.ErrorRowID
	if id <= 0 {
		return ErrInvalidInput
	}
	var classification string
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN NOT (COALESCE(e.status_code,0)>=400 OR e.error_type='cyber_policy') AND NOT (`+providerHealthRow+`) THEN 'not_visible' ELSE `+errorClassification+` END FROM public.ops_error_logs e WHERE e.id=$1 AND e.created_at >= $2 AND e.created_at < $3`, id, q.window.Start, q.window.End).Scan(&classification)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if classification == "not_visible" {
		return ErrNotFound
	}
	if classification == "recovered_upstream" || classification == "unknown" {
		if err = q.require(ctx, "GET /ops/upstream-errors/:id"); err != nil {
			return err
		}
	}
	// Fixed projected scalar columns are selected in PostgreSQL. No whole event,
	// response, message, detail, URL or headers can enter the Go process.
	stmt := `SELECT e.id AS error_row_id,e.created_at,e.request_id,e.client_request_id,e.group_id,ordinal AS attempt_index,
 ` + numericJSON("at_unix_ms") + ` AS at_unix_ms,` + numericJSON("account_id") + ` AS account_id,` + numericJSON("upstream_status_code") + ` AS upstream_status_code,
 ` + numericJSON("attempt_elapsed_ms") + ` AS attempt_elapsed_ms,` + numericJSON("timeout_ms") + ` AS timeout_ms,` + numericJSON("dropped_earlier_attempts") + ` AS dropped_earlier_attempts,
 ` + booleanJSON("would_budget_cut") + ` AS would_budget_cut,` + booleanJSON("budget_cut_applied") + ` AS budget_cut_applied,
 ` + enumJSON("platform", "openai", "anthropic", "gemini", "antigravity", "grok", "kiro", "kimi", "glm", "deepseek") + ` AS platform,
 ` + enumJSON("kind", "http_error", "request_error", "retry_exhausted", "failover", "first_output_timeout", "stream_failed", "stream_error") + ` AS kind,
 ` + enumJSON("stage", "account_auth", "inference", "upload", "first_output", "stream") + ` AS stage,
 ` + enumJSON("scope", "account", "provider", "request", "model", "group", "site") + ` AS scope,
 ` + enumJSON("reason", "first_output_timeout", "stream_failed", "stream_error", "state_rejection", "rate_limited", "overloaded", "balance_exhausted", "account_auth_failed", "request_error", "retry_exhausted", "upstream_error", "timeout", "planned_first_output_timeout", "stream_read_error", "credential_failure", "post_output", "grok_service_temporarily_unavailable", "request_scoped", "request_scoped_transient", "openai_web_search_history_rejected", "openai_alpha_search_e5001_server_error", "openai_request_body_too_large", "openai_upstream_access_state", "openai_http_continuation_unsupported", "grok_search_token", "grok_search_transport", "grok_search_read") + ` AS reason,
 ` + resilienceCategoryJSON() + ` AS resilience_category,
 'ops_error_logs.upstream_errors'::text AS source,'same_storage_row_and_ordinal'::text AS evidence,'exact'::text AS relation_confidence,
 e.created_at AS _page_time,ordinal AS _page_id,'ops_error_logs.upstream_errors'::text AS _page_source
 FROM public.ops_error_logs e CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.upstream_errors)='array' THEN e.upstream_errors ELSE '[]'::jsonb END) WITH ORDINALITY AS events(ev,ordinal)
 WHERE e.id=$1 AND e.created_at >= $2 AND e.created_at < $3`
	q.source("ops_error_logs.upstream_errors")
	if err = q.paged(ctx, tx, stmt, []any{id, q.window.Start, q.window.End}); err != nil {
		return err
	}
	return nil
}
func (q *readerQuery) ingress(ctx context.Context, tx *sql.Tx) error {
	args := []any{q.window.Start, q.window.End}
	w := []string{"bucket_start >= $1", "bucket_start < $2"}
	if q.request.Input.Reason != "" {
		addEqual(&w, &args, "reject_reason", q.request.Input.Reason)
	}
	q.source("ops_ingress_reject_aggregates")
	stmt := `SELECT bucket_start,reject_reason AS reason,route_family,protocol,SUM(request_count)::bigint AS count,MIN(id) AS _page_id,bucket_start AS _page_time,(reject_reason || ':' || route_family || ':' || protocol) AS _page_source FROM public.ops_ingress_reject_aggregates WHERE ` + andWhere(w) + ` GROUP BY bucket_start,reject_reason,route_family,protocol`
	return q.paged(ctx, tx, stmt, args)
}
func (q *readerQuery) ownKey(ctx context.Context, tx *sql.Tx, id int64) error {
	var own int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM public.api_keys WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, id, q.request.Actor.UserID).Scan(&own)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}
