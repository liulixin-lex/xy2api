package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"go.uber.org/zap"
)

const clientRequestIDHeader = "X-Client-Request-ID"

// ClientRequestID ensures every request has a unique client_request_id in request.Context().
//
// This is used by the Ops monitoring module for end-to-end request correlation.
func ClientRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		if v, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(v) != "" {
			var valid bool
			v, valid = normalizeCorrelationID(v)
			if !valid {
				v = uuid.New().String()
			}
			c.Header(clientRequestIDHeader, v)
			ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, v)
			ctx = withResilienceRequestState(ctx, c.Request)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			return
		}

		id := uuid.New().String()
		c.Header(clientRequestIDHeader, id)
		ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, id)
		requestLogger := logger.FromContext(ctx).With(zap.String("client_request_id", strings.TrimSpace(id)))
		ctx = logger.IntoContext(ctx, requestLogger)
		ctx = withResilienceRequestState(ctx, c.Request)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// withResilienceRequestState attaches the per-request gateway resilience
// state and request budget (xy2api). Every gateway route runs this
// middleware first.
func withResilienceRequestState(ctx context.Context, r *http.Request) context.Context {
	if resilience.StateFrom(ctx) != nil {
		return ctx
	}
	st := resilience.NewRequestState()
	if kind, ok := resilienceRequestKind(r); ok {
		st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), kind, time.Now()))
	}
	return resilience.WithRequestState(ctx, st)
}

// resilienceRequestKind selects the budget shape of a request (design §6.1):
// image and video generation have no pre-output deadline, and WebSocket
// connections get no connection-wide budget.
func resilienceRequestKind(r *http.Request) (resilience.RequestKind, bool) {
	if r == nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return resilience.KindText, false
	}
	path := r.URL.Path
	if strings.Contains(path, "/images/") || strings.HasSuffix(path, "/images") || strings.Contains(path, "/videos") {
		return resilience.KindImage, true
	}
	return resilience.KindText, true
}
