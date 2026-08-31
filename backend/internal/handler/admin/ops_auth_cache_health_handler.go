package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
)

// GetAuthCacheInvalidationHealth exposes durable outbox lag and subscriber health.
func (h *OpsHandler) GetAuthCacheInvalidationHealth(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.opsService.GetAuthCacheInvalidationHealth(c.Request.Context()))
}
