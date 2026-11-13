package service

import (
	"context"
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
)

// GatewayUnbilledRequest belongs here only when no billing task was arranged.
// RequestID is the gateway UUID, never a billing or client correlation key.
type GatewayUnbilledRequest struct {
	ID                  int64
	RequestID           string
	CreatedAt           time.Time
	UserID              int64
	APIKeyID            int64
	GroupID             *int64
	AccountID           *int64
	Platform            string
	Model               string
	Stream              bool
	RequestType         *int16
	Result              int16
	ErrorClass          string
	HTTPStatus          *int
	Switches            int16
	Fallback            bool
	Takeover            bool
	FirstOutputMs       *int
	DurationMs          int
	InputTokens         *int
	CacheReadTokens     *int
	CacheCreationTokens *int
}

// GatewayRequestRepository extends the existing usage repository without
// changing the financial/user-side repository contract.
type GatewayRequestRepository interface {
	CreateUnbilledRequest(context.Context, *GatewayUnbilledRequest) error
	GroupLast60(context.Context, []int64, time.Time) (map[int64]usagestats.GroupLast60, error)
}

func (s *UsageService) GroupLast60(ctx context.Context, groupIDs []int64, at time.Time) (map[int64]usagestats.GroupLast60, error) {
	repo, ok := s.usageRepo.(GatewayRequestRepository)
	if !ok {
		return nil, fmt.Errorf("gateway request repository unavailable")
	}
	return repo.GroupLast60(ctx, groupIDs, at)
}
