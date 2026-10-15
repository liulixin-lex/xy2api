package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// recordResilienceUndelivered writes the record of a request whose client
// left before any semantic output (design §8.2). It runs after the handler
// returned; a request is recorded at most once.
func recordResilienceUndelivered(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	if st == nil {
		return
	}
	if st.ClientGone() {
		st.ResolveDisconnect()
	}
	if !st.ClaimUndeliveredRecord() {
		return
	}
	stage, elapsed := st.DisconnectInfo()
	usageJSON, upstreamCost, billed := st.UndeliveredUsage()
	clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	rec := &service.UndeliveredRequest{
		RequestID:             strings.TrimSpace(requestID),
		ClientRequestID:       strings.TrimSpace(clientRequestID),
		InboundEndpoint:       GetInboundEndpoint(c),
		DisconnectStage:       stage,
		DurationMs:            elapsed.Milliseconds(),
		UpstreamUsage:         usageJSON,
		EstimatedUpstreamCost: upstreamCost,
		Billed:                billed,
	}
	if v, ok := c.Get(opsModelKey); ok {
		rec.Model, _ = v.(string)
	}
	if v, ok := c.Get(opsAccountIDKey); ok {
		if id, ok := v.(int64); ok && id > 0 {
			rec.AccountID = &id
		}
	}
	apiKey := getOpsAPIKey(c)
	if apiKey != nil {
		keyID := apiKey.ID
		rec.APIKeyID = &keyID
		rec.GroupID = apiKey.GroupID
		if apiKey.User != nil {
			userID := apiKey.User.ID
			rec.UserID = &userID
		}
	}
	rec.Platform = resolveOpsPlatform(c.Request.Context(), apiKey, guessPlatformFromPath(c.Request.URL.Path))
	service.RecordUndeliveredRequest(rec)
}

// applyResilienceDisconnectFields adds the disconnect stage, delivered bytes
// and elapsed time to the ops record of a client disconnect (design §8.2).
func applyResilienceDisconnectFields(c *gin.Context, entry *service.OpsInsertErrorLogInput) {
	if c == nil || c.Request == nil || entry == nil {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	disconnected, _, _ := st.Disconnect()
	if !disconnected {
		return
	}
	stage, elapsed := st.DisconnectInfo()
	if stage != "" {
		entry.DisconnectStage = &stage
	}
	delivered := st.DeliveredBytes()
	entry.DeliveredBytes = &delivered
	if entry.ResponseLatencyMs == nil && elapsed > 0 {
		ms := elapsed.Milliseconds()
		entry.ResponseLatencyMs = &ms
	}
}
