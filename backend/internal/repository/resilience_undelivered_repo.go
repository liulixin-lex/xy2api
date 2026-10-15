package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// NewUndeliveredRepository stores requests abandoned before any output
// (gateway resilience, migration 274).
func NewUndeliveredRepository(db *sql.DB) service.UndeliveredRepository {
	return &resilienceRepository{db: db}
}

func (r *resilienceRepository) InsertUndelivered(ctx context.Context, rec *service.UndeliveredRequest) error {
	if rec == nil {
		return nil
	}
	var usage any
	if len(rec.UpstreamUsage) > 0 {
		usage = string(rec.UpstreamUsage)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO gateway_undelivered_requests (
			request_id, client_request_id, user_id, api_key_id, group_id, account_id,
			platform, model, inbound_endpoint, disconnect_stage, duration_ms,
			upstream_usage_json, estimated_upstream_cost, billed
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14)`,
		truncateRunes(rec.RequestID, 128), truncateRunes(rec.ClientRequestID, 128),
		nullInt64Ptr(rec.UserID), nullInt64Ptr(rec.APIKeyID), nullInt64Ptr(rec.GroupID), nullInt64Ptr(rec.AccountID),
		truncateRunes(rec.Platform, 32), truncateRunes(rec.Model, 255), truncateRunes(rec.InboundEndpoint, 128),
		truncateRunes(rec.DisconnectStage, 32), rec.DurationMs, usage, rec.EstimatedUpstreamCost, rec.Billed,
	)
	if err != nil {
		return fmt.Errorf("insert undelivered request: %w", err)
	}
	return nil
}

func (r *resilienceRepository) ListUndelivered(ctx context.Context, filter service.UndeliveredFilter, page pagination.PaginationParams) ([]service.UndeliveredRequest, *pagination.PaginationResult, error) {
	if page.Page < 1 {
		page.Page = 1
	}
	if page.PageSize < 1 || page.PageSize > 200 {
		page.PageSize = 50
	}
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if filter.UserID > 0 {
		add("user_id = $%d", filter.UserID)
	}
	if filter.APIKeyID > 0 {
		add("api_key_id = $%d", filter.APIKeyID)
	}
	if filter.GroupID > 0 {
		add("group_id = $%d", filter.GroupID)
	}
	if !filter.Since.IsZero() {
		add("created_at >= $%d", filter.Since)
	}
	if !filter.Until.IsZero() {
		add("created_at < $%d", filter.Until)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gateway_undelivered_requests"+clause, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count undelivered requests: %w", err)
	}
	args = append(args, page.PageSize, (page.Page-1)*page.PageSize)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, request_id, client_request_id, user_id, api_key_id, group_id, account_id,
		       platform, model, inbound_endpoint, disconnect_stage, duration_ms,
		       COALESCE(upstream_usage_json::text, ''), estimated_upstream_cost, billed, created_at
		FROM gateway_undelivered_requests`+clause+fmt.Sprintf(`
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list undelivered requests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.UndeliveredRequest
	for rows.Next() {
		var rec service.UndeliveredRequest
		var userID, keyID, groupID, accountID sql.NullInt64
		var usage string
		if err := rows.Scan(&rec.ID, &rec.RequestID, &rec.ClientRequestID, &userID, &keyID, &groupID, &accountID,
			&rec.Platform, &rec.Model, &rec.InboundEndpoint, &rec.DisconnectStage, &rec.DurationMs,
			&usage, &rec.EstimatedUpstreamCost, &rec.Billed, &rec.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("scan undelivered request: %w", err)
		}
		rec.UserID = int64PtrFromNull(userID)
		rec.APIKeyID = int64PtrFromNull(keyID)
		rec.GroupID = int64PtrFromNull(groupID)
		rec.AccountID = int64PtrFromNull(accountID)
		if usage != "" {
			rec.UpstreamUsage = []byte(usage)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	pages := int((total + int64(page.PageSize) - 1) / int64(page.PageSize))
	return out, &pagination.PaginationResult{Total: total, Page: page.Page, PageSize: page.PageSize, Pages: pages}, nil
}

func (r *resilienceRepository) DeleteUndeliveredBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM gateway_undelivered_requests WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete undelivered requests: %w", err)
	}
	return res.RowsAffected()
}

func int64PtrFromNull(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
