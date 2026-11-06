package mcp

import (
	"context"
	"database/sql"
	"strconv"
)

func (q *readerQuery) ops(ctx context.Context, tx *sql.Tx) error {
	switch q.request.Tool {
	case "xy2api.ops.get_error_breakdown", "xy2api.ops.get_trends":
		return q.errorMetrics(ctx, tx)
	case "xy2api.ops.get_realtime_snapshot":
		return q.realtime(ctx, tx)
	}
	in := q.request.Input
	args := []any{q.window.Start, q.window.End}
	w := []string{"e.created_at >= $1", "e.created_at < $2"}
	var stmt string
	switch q.request.Tool {
	case "xy2api.ops.get_system_events":
		q.source("ops_system_logs")
		if in.Level != "" {
			switch in.Level {
			case "debug", "info", "warn", "error", "fatal":
				addEqual(&w, &args, "e.level", in.Level)
			default:
				return ErrInvalidInput
			}
		}
		for _, f := range []struct{ c, v string }{{"component", in.Component}, {"request_id", in.RequestID}, {"client_request_id", in.ClientRequestID}} {
			if f.v != "" {
				addEqual(&w, &args, "e."+f.c, f.v)
			}
		}

		stmt = `SELECT e.id AS event_id,e.created_at,CASE WHEN e.level IN ('debug','info','warn','error','fatal','panic') THEN e.level ELSE 'unknown' END AS level,e.component,e.request_id,e.client_request_id,e.created_at AS _page_time,e.id AS _page_id,'ops_system_logs'::text AS _page_source FROM public.ops_system_logs e`
	case "xy2api.ops.get_alerts":
		q.source("ops_alert_events", "ops_alert_rules")
		if in.Status != "" {
			switch in.Status {
			case "firing", "resolved":
				addEqual(&w, &args, "e.status", in.Status)
			default:
				return ErrInvalidInput
			}
		}
		if in.Level != "" {
			switch in.Level {
			case "info", "warning", "critical":
				addEqual(&w, &args, "e.severity", in.Level)
			default:
				return ErrInvalidInput
			}
		}
		if in.Platform != "" {
			addEqual(&w, &args, "e.dimensions->>'platform'", in.Platform)
		}
		if in.GroupID > 0 {
			addEqual(&w, &args, "e.dimensions->>'group_id'", int64String(in.GroupID))
		}
		stmt = `SELECT e.id AS alert_id,e.created_at,e.rule_id,CASE WHEN e.severity IN ('info','warning','critical') THEN e.severity ELSE 'unknown' END AS severity,CASE WHEN e.status IN ('firing','resolved') THEN e.status ELSE 'unknown' END AS status,e.fired_at,e.resolved_at,e.metric_value,e.threshold_value,CASE WHEN r.metric_type IN ('cpu_usage_percent','memory_usage_percent','concurrency_queue_depth','group_available_accounts','group_available_ratio','account_rate_limited_count','account_error_count','account_temp_unscheduled_count','group_rate_limit_ratio','account_error_ratio','overload_account_count','proxy_expired_count','proxy_expiring_soon_count','success_rate','error_rate','upstream_error_rate') THEN r.metric_type ELSE 'unknown' END AS machine_reason,e.created_at AS _page_time,e.id AS _page_id,'ops_alert_events'::text AS _page_source FROM public.ops_alert_events e LEFT JOIN public.ops_alert_rules r ON r.id=e.rule_id`
	case "xy2api.audit.search":
		q.source("audit_logs")
		if in.UserID > 0 {
			addEqual(&w, &args, "e.actor_user_id", in.UserID)
		}
		if in.RequestID != "" {
			addEqual(&w, &args, "e.request_id", in.RequestID)
		}
		if in.Status != "" {
			status, err := strconv.Atoi(in.Status)
			if err != nil || status < 100 || status > 599 {
				return ErrInvalidInput
			}
			addEqual(&w, &args, "e.status_code", status)
		}
		stmt = `SELECT e.id AS audit_id,e.created_at,e.actor_user_id,e.action,e.status_code,e.latency_ms,e.request_id,e.created_at AS _page_time,e.id AS _page_id,'audit_logs'::text AS _page_source FROM public.audit_logs e`
	case "xy2api.security.search_prompt_audit":
		q.source("prompt_audit_events")
		if in.RequestID != "" {
			addEqual(&w, &args, "e.request_id", in.RequestID)
		}
		if in.Model != "" {
			addEqual(&w, &args, "e.model", in.Model)
		}
		if in.Platform != "" {
			addEqual(&w, &args, "e.provider", in.Platform)
		}
		if in.UserID > 0 {
			addEqual(&w, &args, "e.user_id", in.UserID)
		}
		if in.GroupID > 0 {
			addEqual(&w, &args, "e.group_id", in.GroupID)
		}
		if in.Status != "" {
			switch in.Status {
			case "pass", "flag", "critical":
				addEqual(&w, &args, "e.decision", in.Status)
			default:
				return ErrInvalidInput
			}
		}
		stmt = `SELECT e.id AS event_id,e.created_at,e.request_id,e.user_id,e.api_key_id,e.group_id,e.provider,e.model,e.stage,e.decision,e.risk_level,e.action,e.latency_ms,e.policy_version,e.config_version,e.created_at AS _page_time,e.id AS _page_id,'prompt_audit_events'::text AS _page_source FROM public.prompt_audit_events e`
	default:
		return ErrInvalidInput
	}
	return q.paged(ctx, tx, stmt+" WHERE "+andWhere(w), args)
}
func (q *readerQuery) metricConditions(alias string, args *[]any) []string {
	w := []string{alias + "created_at >= $1", alias + "created_at < $2"}
	in := q.request.Input
	if in.Platform != "" {
		addEqual(&w, args, alias+"platform", in.Platform)
	}
	if in.GroupID > 0 {
		addEqual(&w, args, alias+"group_id", in.GroupID)
	}
	if in.Model != "" {
		addEqual(&w, args, alias+"model", in.Model)
	}
	return w
}
func (q *readerQuery) errorMetrics(ctx context.Context, tx *sql.Tx) error {
	args := []any{q.window.Start, q.window.End}
	ew := q.metricConditions("e.", &args)
	uw := []string{"ul.created_at >= $1", "ul.created_at < $2"}
	if q.request.Input.Platform != "" {
		addEqual(&uw, &args, "COALESCE(NULLIF(g.platform,''),a.platform)", q.request.Input.Platform)
	}
	if q.request.Input.GroupID > 0 {
		addEqual(&uw, &args, "ul.group_id", q.request.Input.GroupID)
	}
	if q.request.Input.Model != "" {
		addEqual(&uw, &args, "ul.model", q.request.Input.Model)
	}
	q.source("ops_error_logs", "usage_logs")
	q.result.PartialReasons = append(q.result.PartialReasons, "raw_bounded_window; settled_usage_rows_do_not_prove_client_success; error_rows_can_overlap_usage_rows; unique_client_denominator_unknown")
	usageFrom := ` FROM public.usage_logs ul LEFT JOIN public.groups g ON g.id=ul.group_id LEFT JOIN public.accounts a ON a.id=ul.account_id WHERE ` + andWhere(uw)
	if q.request.Tool == "xy2api.ops.get_error_breakdown" {
		stmt := `WITH classified AS (SELECT ` + errorType + ` AS classification,CASE WHEN e.error_owner IN ('client','provider','platform') THEN e.error_owner ELSE 'unknown' END AS owner,e.is_business_limited,` + errorClassification + ` AS outcome FROM public.ops_error_logs e WHERE ` + andWhere(ew) + `), totals AS (SELECT (SELECT COUNT(*)` + usageFrom + `) AS settled_usage_rows), grouped AS (SELECT classification,owner,is_business_limited,COUNT(*) FILTER(WHERE outcome='final_failure')::bigint AS final_client_error_rows,COUNT(*) FILTER(WHERE outcome='recovered_upstream')::bigint AS recovered_upstream_rows,COUNT(*) FILTER(WHERE outcome='unknown')::bigint AS unknown_rows,COUNT(*)::bigint AS source_rows FROM classified GROUP BY 1,2,3) SELECT grouped.*,totals.settled_usage_rows AS settled_usage_rows_in_window,ROW_NUMBER() OVER(ORDER BY classification,owner,is_business_limited) AS _page_id,$2::timestamptz AS _page_time,'error_breakdown'::text AS _page_source FROM grouped CROSS JOIN totals`
		return q.paged(ctx, tx, stmt, args)
	}
	grain := "hour"
	switch q.request.Input.Granularity {
	case "", "hour":
	case "minute":
		grain = "minute"
	case "day":
		grain = "day"
	default:
		return ErrInvalidInput
	}
	bucket := func(column string) string {
		return `date_trunc('` + grain + `',` + column + ` AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`
	}
	stmt := `WITH u AS (SELECT ` + bucket("ul.created_at") + ` AS bucket,COUNT(*)::bigint AS settled_usage_rows` + usageFrom + ` GROUP BY 1),e AS (SELECT ` + bucket("e.created_at") + ` AS bucket,COUNT(*) FILTER(WHERE e.status_code>=400 OR e.error_type='cyber_policy')::bigint AS final_client_error_rows,COUNT(*) FILTER(WHERE (e.status_code>=400 OR e.error_type='cyber_policy') AND e.is_business_limited)::bigint AS business_limited_error_rows,COUNT(*) FILTER(WHERE (e.status_code>=400 OR e.error_type='cyber_policy') AND NOT e.is_business_limited)::bigint AS sla_error_rows,COUNT(*) FILTER(WHERE ` + errorClassification + `='recovered_upstream')::bigint AS recovered_upstream_rows,COUNT(*)::bigint AS source_rows FROM public.ops_error_logs e WHERE ` + andWhere(ew) + ` GROUP BY 1) SELECT COALESCE(u.bucket,e.bucket) AS bucket,COALESCE(u.settled_usage_rows,0) AS settled_usage_rows,COALESCE(e.final_client_error_rows,0) AS final_client_error_rows,COALESCE(e.business_limited_error_rows,0) AS business_limited_error_rows,COALESCE(e.sla_error_rows,0) AS sla_error_rows,COALESCE(e.recovered_upstream_rows,0) AS recovered_upstream_rows,COALESCE(e.source_rows,0) AS error_source_rows,COALESCE(u.bucket,e.bucket) AS _page_time,0::bigint AS _page_id,'ops_error_trend'::text AS _page_source FROM u FULL JOIN e ON u.bucket=e.bucket`
	return q.paged(ctx, tx, stmt, args)
}
func (q *readerQuery) realtime(ctx context.Context, tx *sql.Tx) error {
	args := []any{q.window.Start, q.window.End}
	ew := q.metricConditions("e.", &args)
	uw := []string{"ul.created_at >= $1", "ul.created_at < $2"}
	if q.request.Input.Platform != "" {
		addEqual(&uw, &args, "COALESCE(NULLIF(g.platform,''),a.platform)", q.request.Input.Platform)
	}
	if q.request.Input.GroupID > 0 {
		addEqual(&uw, &args, "ul.group_id", q.request.Input.GroupID)
	}
	q.source("usage_logs", "ops_error_logs")
	q.result.StateScope = "persisted_selected_platform_and_group; source_row_rates_not_unique_client_qps"
	q.result.PartialReasons = append(q.result.PartialReasons, "settled_usage_rows_do_not_prove_client_success; error_rows_can_overlap_usage_rows; unique_client_qps_unknown")
	return q.load(ctx, tx, `WITH u AS (SELECT date_trunc('minute',ul.created_at) AS bucket,COUNT(*) AS settled_usage_rows,SUM(ul.input_tokens::bigint+ul.output_tokens+ul.cache_creation_tokens+ul.cache_read_tokens) AS tokens FROM public.usage_logs ul LEFT JOIN public.groups g ON g.id=ul.group_id LEFT JOIN public.accounts a ON a.id=ul.account_id WHERE `+andWhere(uw)+` GROUP BY 1),e AS (SELECT date_trunc('minute',e.created_at) AS bucket,COUNT(*) AS final_client_error_rows FROM public.ops_error_logs e WHERE `+andWhere(ew)+` AND (COALESCE(e.status_code,0)>=400 OR e.error_type='cyber_policy') GROUP BY 1),combined AS (SELECT COALESCE(u.bucket,e.bucket) AS bucket,COALESCE(u.settled_usage_rows,0) AS settled_usage_rows,COALESCE(e.final_client_error_rows,0) AS final_client_error_rows,COALESCE(u.tokens,0) AS tokens FROM u FULL JOIN e ON u.bucket=e.bucket) SELECT COALESCE(SUM(settled_usage_rows),0)::bigint AS settled_usage_rows,COALESCE(SUM(final_client_error_rows),0)::bigint AS final_client_error_rows,COALESCE(SUM(tokens),0)::bigint AS billed_tokens,COALESCE(SUM(settled_usage_rows),0)::float8/EXTRACT(EPOCH FROM ($2::timestamptz-$1::timestamptz)) AS settled_usage_rows_per_second,COALESCE(SUM(final_client_error_rows),0)::float8/EXTRACT(EPOCH FROM ($2::timestamptz-$1::timestamptz)) AS final_client_error_rows_per_second,COALESCE(SUM(tokens),0)::float8/EXTRACT(EPOCH FROM ($2::timestamptz-$1::timestamptz)) AS billed_tokens_per_second,COALESCE(MAX(settled_usage_rows),0)::float8/60 AS peak_minute_usage_rows_per_second,COALESCE(MAX(final_client_error_rows),0)::float8/60 AS peak_minute_error_rows_per_second FROM combined`, args...)
}

func int64String(v int64) string { return strconv.FormatInt(v, 10) }
