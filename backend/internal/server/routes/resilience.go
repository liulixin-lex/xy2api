package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
)

// registerResilienceRoutes registers the gateway resilience administration
// endpoints (design §10, §12).
func registerResilienceRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.Admin == nil || h.Admin.Resilience == nil {
		return
	}
	r := h.Admin.Resilience
	admin.GET("/settings/gateway-resilience", r.GetSettings)
	admin.PUT("/settings/gateway-resilience", r.UpdateSettings)
}
