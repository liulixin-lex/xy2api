package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
)

func registerCacheObservabilityRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.CacheObservability == nil {
		return
	}
	panel := admin.Group("/cache")
	read := h.Admin.CacheObservability
	panel.GET("/northstar", read.Read("northstar"))
	panel.GET("/breakdown", read.Read("breakdown"))
	panel.GET("/stats", read.Read("stats"))
	panel.GET("/experiments", read.Read("experiments"))
}
