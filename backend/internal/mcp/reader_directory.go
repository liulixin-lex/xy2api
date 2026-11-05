package mcp

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
)

func (q *readerQuery) directory(ctx context.Context, tx *sql.Tx) error {
	in := q.request.Input
	args := []any{}
	w := []string{}
	var stmt string
	switch q.request.Tool {
	case "xy2api.accounts.search":
		w = append(w, "a.deleted_at IS NULL")
		if in.AccountID > 0 {
			addEqual(&w, &args, "a.id", in.AccountID)
		}
		if in.GroupID > 0 {
			addEqual(&w, &args, "ag_filter.group_id", in.GroupID)
		}
		if in.Platform != "" {
			addEqual(&w, &args, "a.platform", in.Platform)
		}
		if in.Status != "" {
			addEqual(&w, &args, "a.status", in.Status)
		}
		if in.Search != "" {
			args = append(args, "%"+escapeReaderLike(in.Search)+"%")
			w = append(w, "a.name ILIKE $"+intString(len(args))+" ESCAPE '\\'")
		}
		stmt = `SELECT a.id AS account_id,a.name,a.platform,a.type,a.status,ARRAY(SELECT ag.group_id FROM public.account_groups ag JOIN public.groups g ON g.id=ag.group_id AND g.deleted_at IS NULL WHERE ag.account_id=a.id ORDER BY ag.group_id LIMIT 100) AS group_ids,a.created_at AS _page_time,a.id AS _page_id,'accounts'::text AS _page_source FROM public.accounts a `
		if in.GroupID > 0 {
			stmt += `JOIN public.account_groups ag_filter ON ag_filter.account_id=a.id `
		}
		q.source("accounts", "account_groups")
	case "xy2api.groups.search":
		w = append(w, "g.deleted_at IS NULL")
		if in.GroupID > 0 {
			addEqual(&w, &args, "g.id", in.GroupID)
		}
		if in.Platform != "" {
			addEqual(&w, &args, "g.platform", in.Platform)
		}
		if in.Status != "" {
			addEqual(&w, &args, "g.status", in.Status)
		}
		if in.Search != "" {
			args = append(args, "%"+escapeReaderLike(in.Search)+"%")
			w = append(w, "g.name ILIKE $"+intString(len(args))+" ESCAPE '\\'")
		}
		stmt = `SELECT g.id AS group_id,g.name,g.platform,g.status,ARRAY(SELECT cm.category_id FROM public.group_category_members cm WHERE cm.group_id=g.id ORDER BY cm.category_id LIMIT 100) AS category_ids,g.created_at AS _page_time,g.id AS _page_id,'groups'::text AS _page_source FROM public.groups g `
		q.source("groups", "group_category_members")
	case "xy2api.channels.search":
		if in.Status != "" {
			addEqual(&w, &args, "c.status", in.Status)
		}
		if in.GroupID > 0 {
			args = append(args, in.GroupID)
			w = append(w, "EXISTS(SELECT 1 FROM public.channel_groups cg WHERE cg.channel_id=c.id AND cg.group_id=$"+intString(len(args))+")")
		}
		if in.Search != "" {
			args = append(args, "%"+escapeReaderLike(in.Search)+"%")
			w = append(w, "c.name ILIKE $"+intString(len(args))+" ESCAPE '\\'")
		}
		stmt = `SELECT c.id AS channel_id,c.name,c.status,ARRAY(SELECT cg.group_id FROM public.channel_groups cg JOIN public.groups g ON g.id=cg.group_id AND g.deleted_at IS NULL WHERE cg.channel_id=c.id ORDER BY cg.group_id LIMIT 100) AS group_ids,c.created_at AS _page_time,c.id AS _page_id,'channels'::text AS _page_source FROM public.channels c `
		q.source("channels", "channel_groups")
	case "xy2api.users.search":
		w = append(w, "u.deleted_at IS NULL", "u.role='user'")
		if in.GroupID > 0 {
			args = append(args, in.GroupID)
			w = append(w, "EXISTS(SELECT 1 FROM public.user_allowed_groups ug WHERE ug.user_id=u.id AND ug.group_id=$"+intString(len(args))+")")
		}
		if in.UserID > 0 {
			addEqual(&w, &args, "u.id", in.UserID)
		}
		if in.Status != "" {
			addEqual(&w, &args, "u.status", in.Status)
		}
		if in.Search != "" {
			if strings.HasPrefix(strings.ToLower(in.Search), "sk-") || strings.Contains(in.Search, "@") {
				return ErrInvalidInput
			}
			args = append(args, "%"+escapeReaderLike(in.Search)+"%")
			w = append(w, "u.username ILIKE $"+intString(len(args))+" ESCAPE '\\'")
		}
		stmt = `SELECT u.id AS user_id,u.username,u.status,u.created_at AS _page_time,u.id AS _page_id,'users'::text AS _page_source FROM public.users u `
		q.source("users")
	default:
		return ErrInvalidInput
	}
	q.result.Window = nil
	q.noHistoricalRetention()
	q.result.PartialReasons = append(q.result.PartialReasons, "nested_group_and_category_bindings_limited_to_100")
	return q.paged(ctx, tx, stmt+"WHERE "+andWhere(w), args)
}
func escapeReaderLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}
func intString(n int) string { return strconv.Itoa(n) }

func (q *readerQuery) groupUsage(ctx context.Context, tx *sql.Tx) error {
	args := []any{q.window.Start, q.window.End}
	w := []string{`r.bucket_date >= ($1::timestamptz AT TIME ZONE s.timezone_name)::date`, `(r.bucket_date::timestamp AT TIME ZONE s.timezone_name) < $2::timestamptz`, `r.bucket_date < s.closed_before`, `g.deleted_at IS NULL`}
	if q.request.Input.GroupID > 0 {
		addEqual(&w, &args, "r.group_id", q.request.Input.GroupID)
	}
	if q.request.Input.Platform != "" {
		addEqual(&w, &args, "g.platform", q.request.Input.Platform)
	}
	q.source("usage_group_daily_rollups", "usage_group_rollup_state")
	q.result.PartialReasons = append(q.result.PartialReasons, "whole_calendar_day_cost_buckets; request_and_token_totals_not_stored")
	stmt := `SELECT r.group_id,r.bucket_date,r.actual_cost,r.computed_at,s.timezone_name,s.closed_before,s.retained_from,(r.bucket_date::timestamp AT TIME ZONE s.timezone_name) AS _page_time,r.group_id AS _page_id,'usage_group_daily_rollups'::text AS _page_source FROM public.usage_group_daily_rollups r CROSS JOIN public.usage_group_rollup_state s JOIN public.groups g ON g.id=r.group_id WHERE s.id=1 AND ` + andWhere(w)
	var watermark, retainedFrom sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT closed_before::timestamp AT TIME ZONE timezone_name,retained_from FROM public.usage_group_rollup_state WHERE id=1`).Scan(&watermark, &retainedFrom); err != nil && err != sql.ErrNoRows {
		return err
	}
	if watermark.Valid {
		v := watermark.Time.UTC()
		q.result.AggregationWatermark = &v
		if v.Before(q.window.End) {
			q.knownCoverageLimit("requested_window_exceeds_published_daily_rollup")
		}
	}
	if retainedFrom.Valid && q.window.Start.Before(retainedFrom.Time) {
		q.knownCoverageLimit("window_precedes_retained_rollup_source")
	}
	return q.paged(ctx, tx, stmt, args)
}
func (q *readerQuery) channelMetrics(ctx context.Context, tx *sql.Tx) error {
	args := []any{q.window.Start, q.window.End}
	w := []string{"m.bucket_start >= $1", "m.bucket_start < $2"}
	if in := q.request.Input; in.Platform != "" {
		addEqual(&w, &args, "m.platform", in.Platform)
	}
	if q.request.Input.GroupID > 0 {
		addEqual(&w, &args, "m.group_id", q.request.Input.GroupID)
	}
	if q.request.Input.Model != "" {
		addEqual(&w, &args, "m.model", q.request.Input.Model)
	}
	var enabled bool
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM public.channel_monitor_v2_config WHERE id=1`).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return ErrSourceDisabled
	}
	var through, usageStart, errorStart sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT data_through,usage_coverage_start,error_coverage_start FROM public.channel_monitor_v2_watermarks WHERE id=1`).Scan(&through, &usageStart, &errorStart); err != nil && err != sql.ErrNoRows {
		return err
	}
	q.source("channel_monitor_v2_metrics_1m", "channel_monitor_v2_error_metrics_1m", "channel_monitor_v2_watermarks")
	q.result.PartialReasons = append(q.result.PartialReasons, "passive_real_user_traffic; minute_buckets")
	if through.Valid {
		v := through.Time.UTC()
		q.result.AggregationWatermark = &v
		if v.Before(q.window.End) {
			q.knownCoverageLimit("aggregation_lag")
		}
	} else {
		q.result.PartialReasons = append(q.result.PartialReasons, "aggregation_watermark_unknown")
	}
	if !usageStart.Valid || !errorStart.Valid {
		q.result.PartialReasons = append(q.result.PartialReasons, "aggregation_coverage_start_unknown")
	}
	if (usageStart.Valid && usageStart.Time.After(q.window.Start)) || (errorStart.Valid && errorStart.Time.After(q.window.Start)) {
		q.knownCoverageLimit("window_precedes_known_coverage")
	}
	stmt := `SELECT m.bucket_start,m.platform,m.group_id,m.model,m.success_requests,m.error_requests,m.upstream_affected_requests,m.upstream_attempt_count,m.input_tokens,m.output_tokens,m.cache_creation_tokens,m.cache_read_tokens,m.ttft_count,m.duration_count,CASE WHEN m.ttft_count>0 THEN m.ttft_sum_ms::float8/m.ttft_count END AS ttft_mean_ms,CASE WHEN m.duration_count>0 THEN m.duration_sum_ms::float8/m.duration_count END AS duration_mean_ms,m.computed_at,'passive_real_user_traffic'::text AS observation_mode,m.bucket_start AS _page_time,m.group_id AS _page_id,(m.platform||':'||m.model) AS _page_source FROM public.channel_monitor_v2_metrics_1m m WHERE ` + andWhere(w)
	if err := q.paged(ctx, tx, stmt, args); err != nil {
		return err
	}
	// Errors are independently permissioned in the registry. Aggregate them only
	// for the exact bounded metric cells in this page, preserving taxonomy.
	for _, item := range q.result.Items {
		bucket, ok := item["bucket_start"].(time.Time)
		if !ok {
			return ErrUnavailable
		}
		errors, err := rowMaps(ctx, tx, `SELECT error_category,taxonomy_version,error_requests FROM public.channel_monitor_v2_error_metrics_1m WHERE bucket_start=$1 AND platform=$2 AND group_id=$3 AND model=$4 ORDER BY error_category,taxonomy_version LIMIT 100`, bucket, item["platform"], item["group_id"], item["model"])
		if err != nil {
			return err
		}
		item["error_breakdown"] = errors
	}
	return nil
}
