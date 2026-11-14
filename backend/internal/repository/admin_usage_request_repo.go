package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.AdminUsageRequestRepository = (*usageLogRepository)(nil)

// This projection is private to administrator request history. Financial and
// user-side queries still read only usage_logs. UNION ALL preserves real IDs;
// (source,id) is the stable row key.
const adminRequestsCTE = `WITH requests AS (
	SELECT 'usage'::text AS source, id, gateway_request_id, result, switches, fallback, takeover,
		created_at, user_id, api_key_id, account_id, group_id, model, requested_model, request_id,
		request_type, stream, openai_ws_mode, native_compaction_v2, billing_type, billing_mode,
		upstream_model_mismatch, origin_group_id, upstream_model, model_mapping_chain, image_count,
		end_to_end_first_output_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens,
		cache_creation_tokens, total_cost, actual_cost, account_stats_cost, account_rate_multiplier,
		inbound_endpoint, upstream_endpoint
	FROM usage_logs
	UNION ALL
	SELECT 'unbilled', id, request_id, result, switches, fallback, takeover,
		created_at, user_id, api_key_id, account_id, group_id, model, model, request_id,
		request_type, stream, request_type=3, NULL, NULL, NULL,
		NULL, NULL, NULL, NULL, 0,
		first_output_ms, duration_ms, input_tokens, NULL, cache_read_tokens,
		cache_creation_tokens, NULL, NULL, NULL, NULL, NULL, NULL
	FROM gateway_unbilled_requests
) `

func adminRequestWhere(filters usagestats.UsageLogFilters) (string, []any, error) {
	if filters.StartTime == nil || filters.EndTime == nil || !filters.StartTime.Before(*filters.EndTime) {
		return "", nil, fmt.Errorf("admin request query requires a bounded time window")
	}
	where, args := usageExportWhere(filters)
	if filters.Result != nil {
		if *filters.Result < 1 || *filters.Result > 4 {
			return "", nil, fmt.Errorf("invalid request result")
		}
		args = append(args, *filters.Result)
		where += fmt.Sprintf(" AND result = $%d", len(args))
	}
	if filters.BillingType != nil || filters.BillingMode != "" || filters.NativeCompactionV2 != nil || filters.UpstreamModelMismatch != nil {
		where += " AND source = 'usage'"
	}
	return where, args, nil
}

func (r *usageLogRepository) ListAdminRequests(ctx context.Context, params pagination.PaginationParams, filters usagestats.UsageLogFilters) ([]service.AdminUsageRecord, *pagination.PaginationResult, error) {
	where, args, err := adminRequestWhere(filters)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	limit := params.Limit()
	var total int64
	fast := shouldUseFastUsageLogTotal(filters)
	if !fast {
		if err := scanSingleRow(ctx, r.sql, adminRequestsCTE+"SELECT COUNT(*) FROM requests "+where, args, &total); err != nil {
			return nil, nil, err
		}
	}
	query := adminRequestsCTE + `SELECT source, id, gateway_request_id, result, switches, fallback, takeover,
		created_at, user_id, api_key_id, account_id, group_id, model, request_type, stream,
		end_to_end_first_output_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens
		FROM requests ` + where + " ORDER BY " + adminRequestOrderBy(params)
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := r.sql.QueryContext(ctx, query, append(args, limit+1, params.Offset())...)
	if err != nil {
		return nil, nil, err
	}
	items := make([]service.AdminUsageRecord, 0, limit)
	for rows.Next() {
		var row service.AdminUsageRecord
		var accountID *int64
		var requestType *int16
		l := &row.Log
		if err := rows.Scan(&row.Source, &l.ID, &row.GatewayRequestID, &row.Result, &row.Switches, &row.Fallback, &row.Takeover,
			&l.CreatedAt, &l.UserID, &l.APIKeyID, &accountID, &l.GroupID, &l.Model, &requestType, &l.Stream,
			&l.EndToEndFirstOutputMs, &l.DurationMs, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheCreationTokens); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if accountID != nil {
			l.AccountID = *accountID
		}
		if requestType != nil {
			l.RequestType = service.RequestTypeFromInt16(*requestType)
		}
		l.OpenAIWSMode = l.RequestType == service.RequestTypeWSV2
		l.RequestedModel = l.Model
		if row.GatewayRequestID != nil {
			l.RequestID = *row.GatewayRequestID
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if fast {
		total = int64(params.Offset() + len(items))
	}
	if len(items) > limit {
		items = items[:limit]
	}
	if err := r.hydrateAdminRequests(ctx, items); err != nil {
		return nil, nil, err
	}
	return items, paginationResultFromTotal(total, params), nil
}

func adminRequestOrderBy(params pagination.PaginationParams) string {
	if strings.TrimSpace(params.SortBy) == "" {
		return "created_at DESC, id DESC, source ASC"
	}
	return usageLogOrderBy(params) + ", source ASC"
}

func (r *usageLogRepository) hydrateAdminRequests(ctx context.Context, items []service.AdminUsageRecord) error {
	if len(items) == 0 {
		return nil
	}
	var usageIDs []int64
	var requestIDs []string
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, row := range items {
		if row.Source == "usage" {
			usageIDs = append(usageIDs, row.Log.ID)
		}
		if row.GatewayRequestID != nil && *row.GatewayRequestID != "" && !row.Log.CreatedAt.Before(cutoff) {
			requestIDs = append(requestIDs, *row.GatewayRequestID)
		}
	}
	byID := make(map[int64]service.UsageLog, len(usageIDs))
	if len(usageIDs) > 0 {
		logs, err := r.queryUsageLogs(ctx, "SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE id = ANY($1)", pq.Array(usageIDs))
		if err != nil {
			return err
		}
		for _, log := range logs {
			byID[log.ID] = log
		}
	}
	logs := make([]service.UsageLog, len(items))
	for i := range items {
		logs[i] = items[i].Log
		if items[i].Source == "usage" {
			if log, ok := byID[logs[i].ID]; ok {
				logs[i] = log
			}
		}
		logs[i].FirstTokenMs = nil
		if logs[i].EndToEndFirstOutputMs != nil {
			ms := int(*logs[i].EndToEndFirstOutputMs)
			logs[i].FirstTokenMs = &ms
		}
	}
	if err := r.hydrateUsageLogAssociations(ctx, logs); err != nil {
		return err
	}
	for i := range items {
		items[i].Log = logs[i]
	}
	if len(requestIDs) > 0 {
		rows, err := r.sql.QueryContext(ctx, `SELECT DISTINCT request_id FROM account_attempt_samples WHERE request_id = ANY($1) AND started_at >= $2`, pq.Array(requestIDs), cutoff)
		if err != nil {
			return err
		}
		has := make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			has[id] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		for i := range items {
			if items[i].GatewayRequestID != nil && !items[i].Log.CreatedAt.Before(cutoff) {
				items[i].HasAttempts = has[*items[i].GatewayRequestID]
			}
		}
	}
	return r.annotateAdminTTFO(ctx, items)
}

func (r *usageLogRepository) annotateAdminTTFO(ctx context.Context, items []service.AdminUsageRecord) error {
	var raw string
	err := scanSingleRow(ctx, r.sql, "SELECT value FROM settings WHERE key = $1", []any{"group_quality_gate_policies"}, &raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var policies struct {
		Groups map[string]struct {
			V4 struct {
				Enabled bool    `json:"enabled"`
				Seconds float64 `json:"ttft_seconds"`
			} `json:"v4"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(raw), &policies); err != nil {
		return fmt.Errorf("decode current scheduling policies: %w", err)
	}
	for i := range items {
		l := &items[i].Log
		if l.GroupID == nil || l.FirstTokenMs == nil {
			continue
		}
		p := policies.Groups[strconv.FormatInt(*l.GroupID, 10)].V4
		if p.Enabled && p.Seconds > 0 {
			over := float64(*l.FirstTokenMs) > p.Seconds*1000
			items[i].TTFOOver = &over
		}
	}
	return nil
}

func (r *usageLogRepository) RequestAttempts(ctx context.Context, requestID string, at time.Time) ([]service.UsageAttempt, error) {
	result := make([]service.UsageAttempt, 0)
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	if requestID == "" || at.Before(cutoff) {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Require the displayed row to exist; the caller cannot use an unrelated at
	// to broaden the bounded association lookup.
	var exists bool
	err := scanSingleRow(ctx, r.sql, `SELECT EXISTS (
		SELECT 1 FROM usage_logs WHERE gateway_request_id=$1 AND created_at >= $2 AND created_at <= $3
		UNION ALL SELECT 1 FROM gateway_unbilled_requests WHERE request_id=$1 AND created_at >= $2 AND created_at <= $3
	)`, []any{requestID, at.Add(-time.Millisecond), at.Add(time.Millisecond)}, &exists)
	if err != nil || !exists {
		return result, err
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT s.attempt_id, s.account_id, a.name, s.started_at, s.ttfo_ms,
		(EXTRACT(EPOCH FROM (s.finished_at-s.started_at))*1000)::bigint,
		CASE WHEN s.outcome=4 THEN 3 ELSE s.outcome END, NULLIF(s.detail,'')
		FROM account_attempt_samples s LEFT JOIN accounts a ON a.id=s.account_id
		WHERE s.request_id=$1 AND s.started_at >= $2 AND s.started_at <= $3
		ORDER BY s.started_at, s.attempt_id`, requestID, maxTime(at.Add(-24*time.Hour), cutoff), at.Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row service.UsageAttempt
		if err := rows.Scan(&row.AttemptID, &row.AccountID, &row.AccountName, &row.StartedAt, &row.TTFOMs, &row.DurationMs, &row.Outcome, &row.ErrorClass); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
