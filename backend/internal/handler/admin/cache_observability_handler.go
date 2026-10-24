package admin

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// A read-only surface, separate from user DTOs and all policy mutation methods.
type CachePanelReader interface {
	Query(context.Context, string, service.CachePanelQuery) (any, error)
}
type CacheObservabilityHandler struct{ panel CachePanelReader }

func NewCacheObservabilityHandler(panel CachePanelReader) *CacheObservabilityHandler {
	return &CacheObservabilityHandler{panel: panel}
}
func (h *CacheObservabilityHandler) Read(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		query, err := service.ParseCachePanelQuery(kind, c.Request.URL.Query())
		var value any
		if err == nil {
			value, err = h.panel.Query(c.Request.Context(), kind, query)
		}
		if err != nil {
			var failure *service.CachePolicyError
			if errors.As(err, &failure) {
				response.ErrorWithDetails(c, failure.Status, failure.Message, failure.Reason, failure.Metadata)
			} else {
				response.Error(c, 500, "cache observability storage unavailable")
			}
			return
		}
		if kind == "northstar" || kind == "breakdown" {
			c.Header("Cache-Control", "private, max-age=60")
		} else {
			c.Header("Cache-Control", "private, no-store")
		}
		response.Success(c, value)
	}
}
