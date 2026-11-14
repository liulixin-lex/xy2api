package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/resilience"
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

func applyGatewayRequestMetadata(ctx context.Context, row *UsageLog) {
	meta := resilience.GatewayRequestMetadataFromContext(ctx)
	if meta == nil || row == nil {
		return
	}
	row.GatewayRequestID, row.Result, row.Switches = &meta.RequestID, &meta.Result, &meta.Switches
	row.Fallback, row.Takeover = &meta.Fallback, &meta.Takeover
	row.EndToEndFirstOutputMs = meta.FirstOutputMs
	duration := int64(meta.DurationMs)
	row.EndToEndDurationMs = &duration
}

func createUnbilledGatewayRequest(ctx context.Context, repo UsageLogRepository, row *GatewayUnbilledRequest) error {
	store, ok := repo.(GatewayRequestRepository)
	if !ok {
		return fmt.Errorf("gateway request repository unavailable")
	}
	return store.CreateUnbilledRequest(ctx, row)
}

func (s *GatewayService) RecordUnbilledGatewayRequest(ctx context.Context, row *GatewayUnbilledRequest) error {
	return createUnbilledGatewayRequest(ctx, s.usageLogRepo, row)
}

func (s *OpenAIGatewayService) RecordUnbilledGatewayRequest(ctx context.Context, row *GatewayUnbilledRequest) error {
	return createUnbilledGatewayRequest(ctx, s.usageLogRepo, row)
}

// RecordGatewayRequestWriteFailure is exposed by the existing operations metric window.
func RecordGatewayRequestWriteFailure() { resilience.Count("gateway_unbilled_write_failure", 0, 0, "") }

// RecordGatewayHTTPAttemptResult records an actual failed send, never merely a
// second send. Body/stream failures are recorded by forwarding terminal hooks.
func RecordGatewayHTTPAttemptResult(ctx context.Context, response *http.Response, err error) {
	if err == nil && response != nil && response.StatusCode < 400 {
		return
	}
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	detail := string(classifyUpstreamOutcome(resilience.Outcome{StatusCode: status, Err: err}).Category)
	resilience.StateFrom(ctx).NoteGatewayAttempt(false, detail, nil, nil, nil)
}
