package mcp

import (
	"context"
	"database/sql"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/timezone"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (q *readerQuery) profile(ctx context.Context, tx *sql.Tx) error {
	q.source("users")
	q.result.Window = nil
	q.noHistoricalRetention()
	err := q.load(ctx, tx, `SELECT id AS user_id,username,role,status,balance,concurrency FROM public.users WHERE id=$1 AND deleted_at IS NULL`, q.request.Actor.UserID)
	if err == nil && len(q.result.Items) == 0 {
		return ErrNotFound
	}
	return err
}
func (q *readerQuery) selfUsage(ctx context.Context, tx *sql.Tx) error {
	in := q.request.Input
	if in.UserID != 0 && in.UserID != q.request.Actor.UserID {
		return ErrInvalidInput
	}
	args := []any{q.window.Start, q.window.End, q.request.Actor.UserID}
	w := []string{"ul.created_at >= $1", "ul.created_at < $2", "ul.user_id=$3"}
	if in.APIKeyID > 0 {
		if err := q.ownKey(ctx, tx, in.APIKeyID); err != nil {
			return err
		}
		addEqual(&w, &args, "ul.api_key_id", in.APIKeyID)
	}
	if in.GroupID > 0 {
		addEqual(&w, &args, "COALESCE(ul.origin_group_id,ul.group_id)", in.GroupID)
	}
	if in.Model != "" {
		addEqual(&w, &args, "COALESCE(NULLIF(ul.requested_model,''),ul.model)", in.Model)
	}
	if in.RequestID != "" {
		addEqual(&w, &args, "ul.request_id", in.RequestID)
	}
	if in.Platform != "" {
		addEqual(&w, &args, "g.platform", in.Platform)
	}
	detail := q.request.Tool == "xy2api.me.get_usage_detail"
	if detail {
		if in.UsageRowID <= 0 {
			return ErrInvalidInput
		}
		addEqual(&w, &args, "ul.id", in.UsageRowID)
	}
	q.source("usage_logs")
	q.result.PartialReasons = append(q.result.PartialReasons, "settled_usage_only; origin_group_semantics")
	stmt := `SELECT ul.id AS usage_row_id,ul.created_at,ul.request_id,COALESCE(NULLIF(ul.requested_model,''),ul.model) AS model,ul.api_key_id,COALESCE(ul.origin_group_id,ul.group_id) AS group_id,g.name AS group_name,ul.input_tokens,ul.output_tokens,ul.cache_creation_tokens,ul.cache_read_tokens,ul.actual_cost,ul.duration_ms,ul.first_token_ms,ul.end_to_end_first_output_ms,ul.stream,CASE WHEN ul.input_tokens+ul.cache_creation_tokens+ul.cache_read_tokens>0 THEN ul.cache_read_tokens::float8/(ul.input_tokens+ul.cache_creation_tokens+ul.cache_read_tokens) END AS billed_cache_read_share,CASE WHEN ul.duration_ms>0 THEN ul.output_tokens::float8*1000/ul.duration_ms END AS average_output_tps,ul.created_at AS _page_time,ul.id AS _page_id,'usage_logs'::text AS _page_source FROM public.usage_logs ul LEFT JOIN public.groups g ON g.id=COALESCE(ul.origin_group_id,ul.group_id) WHERE ` + andWhere(w)
	if err := q.paged(ctx, tx, stmt, args); err != nil {
		return err
	}
	if detail && len(q.result.Items) == 0 {
		return ErrNotFound
	}
	return nil
}
func (q *readerQuery) userUsageSummary(ctx context.Context, tx *sql.Tx) error {
	if q.request.Input.UserID <= 0 {
		return ErrInvalidInput
	}
	args := []any{q.window.Start, q.window.End, q.request.Input.UserID}
	w := []string{"ul.created_at >= $1", "ul.created_at < $2", "ul.user_id=$3"}
	if q.request.Input.GroupID > 0 {
		addEqual(&w, &args, "COALESCE(ul.origin_group_id,ul.group_id)", q.request.Input.GroupID)
	}
	if q.request.Input.Model != "" {
		addEqual(&w, &args, "COALESCE(NULLIF(ul.requested_model,''),ul.model)", q.request.Input.Model)
	}
	if q.request.Input.Platform != "" {
		addEqual(&w, &args, "g.platform", q.request.Input.Platform)
	}
	q.source("usage_logs")
	q.result.PartialReasons = append(q.result.PartialReasons, "settled_usage_only; origin_group_semantics")
	return q.load(ctx, tx, `SELECT $3::bigint AS user_id,COUNT(*)::bigint AS requests,COALESCE(SUM(ul.input_tokens),0)::bigint AS input_tokens,COALESCE(SUM(ul.output_tokens),0)::bigint AS output_tokens,COALESCE(SUM(ul.cache_creation_tokens),0)::bigint AS cache_creation_tokens,COALESCE(SUM(ul.cache_read_tokens),0)::bigint AS cache_read_tokens,COALESCE(SUM(ul.actual_cost),0) AS actual_cost FROM public.usage_logs ul LEFT JOIN public.groups g ON g.id=COALESCE(ul.origin_group_id,ul.group_id) WHERE `+andWhere(w), args...)
}
func (q *readerQuery) subscriptions(ctx context.Context, tx *sql.Tx) error {
	owner := q.request.Actor.UserID
	if q.request.Tool == "xy2api.users.get_subscriptions" {
		owner = q.request.Input.UserID
		if owner <= 0 {
			return ErrInvalidInput
		}
	}
	args := []any{owner}
	w := []string{"s.user_id=$1", "s.deleted_at IS NULL"}
	if q.request.Input.GroupID > 0 {
		addEqual(&w, &args, "s.group_id", q.request.Input.GroupID)
	}
	if q.request.Input.Status != "" {
		addEqual(&w, &args, "s.status", q.request.Input.Status)
	}
	q.source("user_subscriptions", "groups")
	q.result.Window = nil
	q.noHistoricalRetention()
	stmt := `SELECT s.id AS subscription_id,s.group_id,g.name AS group_name,s.starts_at,s.expires_at,s.status,s.daily_window_start,s.weekly_window_start,s.monthly_window_start,s.daily_usage_usd,s.weekly_usage_usd,s.monthly_usage_usd,g.daily_limit_usd,g.weekly_limit_usd,g.monthly_limit_usd,s.created_at AS _page_time,s.id AS _page_id,'user_subscriptions'::text AS _page_source FROM public.user_subscriptions s LEFT JOIN public.groups g ON g.id=s.group_id WHERE ` + andWhere(w)
	if err := q.paged(ctx, tx, stmt, args); err != nil {
		return err
	}
	for _, m := range q.result.Items {
		normalizeSubscriptionItem(m, q.result.GeneratedAt)
	}
	return nil
}
func ptrItemTime(m map[string]any, key string) *time.Time {
	if t, ok := m[key].(time.Time); ok {
		return &t
	}
	return nil
}
func floatItem(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	}
	return 0
}
func normalizeSubscriptionItem(m map[string]any, now time.Time) {
	sub := service.UserSubscription{DailyWindowStart: ptrItemTime(m, "daily_window_start"), WeeklyWindowStart: ptrItemTime(m, "weekly_window_start"), MonthlyWindowStart: ptrItemTime(m, "monthly_window_start")}
	if t := ptrItemTime(m, "starts_at"); t != nil {
		sub.StartsAt = *t
	}
	if t := ptrItemTime(m, "expires_at"); t != nil {
		sub.ExpiresAt = *t
	}
	if m["status"] == "active" && !sub.ExpiresAt.After(now) {
		m["status"] = "expired"
	}
	// Exported reset-time helpers preserve legacy periodic anchors and one-time
	// cards. Reset eligibility is calculated without calling any service writer.
	resets := map[string]*time.Time{"daily": sub.DailyResetTime(), "weekly": sub.WeeklyResetTime(), "monthly": sub.MonthlyResetTime()}
	for _, kind := range []string{"daily", "weekly", "monthly"} {
		reset := resets[kind]
		expired := reset != nil && !now.Before(*reset)
		if kind == "daily" && sub.HasOneTimeDailyQuota() {
			expired = false
		}
		if kind != "daily" && reset != nil && !reset.Before(sub.ExpiresAt) {
			expired = false
		}
		if expired {
			m[kind+"_usage_usd"] = float64(0)
			m[kind+"_window_start"] = nil
			m[kind+"_resets_at"] = nil
		} else {
			m[kind+"_resets_at"] = reset
		}
		usage := floatItem(m, kind+"_usage_usd")
		if limit, ok := m[kind+"_limit_usd"].(float64); ok {
			m[kind+"_remaining_usd"] = max(float64(0), limit-usage)
		}
	}
}
func (q *readerQuery) quotas(ctx context.Context, tx *sql.Tx) error {
	args := []any{q.request.Actor.UserID}
	w := []string{"user_id=$1", "deleted_at IS NULL"}
	if q.request.Input.Platform != "" {
		addEqual(&w, &args, "platform", q.request.Input.Platform)
	}
	q.source("user_platform_quotas")
	q.result.Window = nil
	q.noHistoricalRetention()
	q.result.PartialReasons = append(q.result.PartialReasons, "persisted_quota_snapshot; pending_cache_usage_not_included")
	if err := q.paged(ctx, tx, `SELECT id AS quota_id,platform,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start,updated_at AS observed_at,created_at AS _page_time,id AS _page_id,'user_platform_quotas'::text AS _page_source FROM public.user_platform_quotas WHERE `+andWhere(w), args); err != nil {
		return err
	}
	now := q.result.GeneratedAt
	for _, m := range q.result.Items {
		status := "available"
		for _, kind := range []string{"daily", "weekly", "monthly"} {
			start := ptrItemTime(m, kind+"_window_start")
			expired := start == nil
			var reset *time.Time
			if start != nil {
				var t time.Time
				switch kind {
				case "daily":
					expired = start.Before(timezone.StartOfDay(now))
					t = timezone.StartOfDay(*start).AddDate(0, 0, 1)
				case "weekly":
					expired = start.Before(timezone.StartOfWeek(now))
					t = timezone.StartOfWeek(*start).AddDate(0, 0, 7)
				case "monthly":
					expired = !now.Before(start.Add(30 * 24 * time.Hour))
					t = start.Add(30 * 24 * time.Hour)
				}
				reset = &t
			}
			if expired {
				m[kind+"_usage_usd"] = float64(0)
				m[kind+"_window_start"] = nil
				reset = nil
			}
			m[kind+"_resets_at"] = reset
			if limit, ok := m[kind+"_limit_usd"].(float64); ok {
				usage := floatItem(m, kind+"_usage_usd")
				m[kind+"_remaining_usd"] = max(float64(0), limit-usage)
				if usage >= limit {
					status = "limited"
				}
			}
		}
		m["status"] = status
	}
	return nil
}
