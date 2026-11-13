package service

import (
	"context"
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
)

// AdminUsageRecord preserves financial DTOs while representing unknown fields
// on requests that never arranged a bill.
type AdminUsageRecord struct {
	Log                 UsageLog
	Source              string
	GatewayRequestID    *string
	Result              *int16
	Switches            *int16
	Fallback            *bool
	Takeover            *bool
	TTFOOver            *bool
	HasAttempts         bool
	InputTokens         *int
	OutputTokens        *int
	CacheReadTokens     *int
	CacheCreationTokens *int
}

type UsageAttempt struct {
	AttemptID   string    `json:"attempt_id"`
	AccountID   int64     `json:"account_id"`
	AccountName *string   `json:"account_name"`
	StartedAt   time.Time `json:"started_at"`
	TTFOMs      *int      `json:"ttfo_ms"`
	DurationMs  *int      `json:"duration_ms"`
	Outcome     int16     `json:"outcome"`
	ErrorClass  *string   `json:"error_class"`
}

type AdminUsageRequestRepository interface {
	ListAdminRequests(context.Context, pagination.PaginationParams, usagestats.UsageLogFilters) ([]AdminUsageRecord, *pagination.PaginationResult, error)
	AdminRequestStats(context.Context, usagestats.UsageLogFilters) (*usagestats.UsageStats, error)
	RequestAttempts(context.Context, string, time.Time) ([]UsageAttempt, error)
}

func (s *UsageService) ListAdminRequests(ctx context.Context, params pagination.PaginationParams, filters usagestats.UsageLogFilters) ([]AdminUsageRecord, *pagination.PaginationResult, error) {
	repo, ok := s.usageRepo.(AdminUsageRequestRepository)
	if !ok {
		return nil, nil, fmt.Errorf("admin request repository unavailable")
	}
	return repo.ListAdminRequests(ctx, params, filters)
}

func (s *UsageService) AdminRequestStats(ctx context.Context, filters usagestats.UsageLogFilters) (*usagestats.UsageStats, error) {
	repo, ok := s.usageRepo.(AdminUsageRequestRepository)
	if !ok {
		return nil, fmt.Errorf("admin request repository unavailable")
	}
	return repo.AdminRequestStats(ctx, filters)
}

func (s *UsageService) RequestAttempts(ctx context.Context, requestID string, at time.Time) ([]UsageAttempt, error) {
	repo, ok := s.usageRepo.(AdminUsageRequestRepository)
	if !ok {
		return nil, fmt.Errorf("admin request repository unavailable")
	}
	return repo.RequestAttempts(ctx, requestID, at)
}
