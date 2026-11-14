package handler

import (
	"context"
	"io"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

type gatewayRequestWriter interface {
	RecordUnbilledGatewayRequest(context.Context, *service.GatewayUnbilledRequest) error
}

// Admission excludes authentication, malformed bodies and local policy rejects.
// The closure reads the final API key because failover may change its group.
func beginGatewayRequest(c *gin.Context, key func() *service.APIKey, model string, stream bool, writer gatewayRequestWriter) func() {
	st := resilience.StateFrom(c.Request.Context())
	if st == nil {
		st = resilience.NewRequestState()
		st.InitDelivery(c.Request.Context(), time.Now(), nil)
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
	}
	st.EnableGatewayRequest(time.Now())
	return func() { finishUnbilledGatewayRequest(c, key(), model, stream, false, writer) }
}

func gatewayBillingContext(ctx context.Context) context.Context {
	st := resilience.StateFrom(ctx)
	if st == nil || !st.GatewayRequestEnabled() {
		return ctx
	}
	meta, _ := st.ClaimGatewayRequest(true)
	return resilience.WithGatewayRequestMetadata(ctx, meta)
}

func finishUnbilledGatewayRequest(c *gin.Context, key *service.APIKey, model string, stream, ws bool, writer gatewayRequestWriter) {
	if c == nil || c.Request == nil || key == nil {
		return
	}
	service.FinishSchedAttemptAtHandlerExit(c.Request.Context())
	meta, write := resilience.StateFrom(c.Request.Context()).ClaimGatewayRequest(false)
	if !write || meta == nil {
		return
	}
	key = service.BillingAPIKey(c.Request.Context(), key)
	requestType := int16(service.RequestTypeFromLegacy(stream, ws))
	status := c.Writer.Status()
	row := &service.GatewayUnbilledRequest{
		RequestID: meta.RequestID, CreatedAt: meta.StartedAt, UserID: key.UserID, APIKeyID: key.ID, GroupID: key.GroupID,
		Model: model, Stream: stream, RequestType: &requestType, Result: meta.Result, ErrorClass: meta.ErrorClass,
		HTTPStatus: &status, Switches: meta.Switches, Fallback: meta.Fallback, Takeover: meta.Takeover,
		DurationMs: meta.DurationMs, InputTokens: meta.InputTokens, CacheReadTokens: meta.CacheReadTokens, CacheCreationTokens: meta.CacheCreationTokens,
	}
	if meta.AccountID > 0 {
		row.AccountID = &meta.AccountID
	}
	if meta.FirstOutputMs != nil {
		n := int(*meta.FirstOutputMs)
		row.FirstOutputMs = &n
	}
	if key.Group != nil {
		row.Platform = key.Group.Platform
	}
	if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
		row.Platform = platform
	}
	// Same synchronous detached tail and timeout as billing; no lossy queue.
	runDurableUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := writer.RecordUnbilledGatewayRequest(ctx, row); err != nil {
			service.RecordGatewayRequestWriteFailure()
			requestLogger(c, "handler.gateway.requests").Error("gateway.unbilled_write_failed", zap.String("gateway_request_id", row.RequestID), zap.Error(err))
		}
	}, "handler.gateway.requests")
}

func recordGatewayForwardUsage(c *gin.Context, result *service.ForwardResult) {
	if result == nil {
		return
	}
	u := result.Usage
	if u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens <= 0 {
		return
	}
	resilience.StateFrom(c.Request.Context()).NoteGatewayAttempt(true, "", &u.InputTokens, &u.CacheReadInputTokens, &u.CacheCreationInputTokens)
}
func recordOpenAIForwardUsage(c *gin.Context, account *service.Account, result *service.OpenAIForwardResult) {
	// OpenAI's send capture already records each send, including failed sends.
	if result == nil || account == nil || account.Platform == service.PlatformOpenAI || result.Usage.InputTokens <= 0 {
		return
	}
	u := result.Usage
	input := max(u.InputTokens-u.CacheReadInputTokens-u.CacheCreationInputTokens, 0)
	resilience.StateFrom(c.Request.Context()).NoteGatewayAttempt(true, "", &input, &u.CacheReadInputTokens, &u.CacheCreationInputTokens)
}

func gatewayOpenAICompleted(result *service.OpenAIForwardResult) bool {
	return result.CompletedForDelivery()
}

func finishOpenAIStickyForward(c *gin.Context, accountID int64, result *service.OpenAIForwardResult, err error) {
	if err == nil && service.GatewayDeliveryFailed(c) {
		err = io.ErrShortWrite
	}
	service.FinishOpenAIStickyForward(c.Request.Context(), accountID, result, err)
}
