package routes

import (
	"github.com/liulixin-lex/xy2api/internal/handler"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"

	"github.com/gin-gonic/gin"
)

// registerShareSchedulingRoutes serves the share-scheduling admin API.
func registerShareSchedulingRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.Admin == nil || h.Admin.ShareScheduling == nil {
		return
	}
	share := admin.Group("/share-scheduling")
	{
		share.GET("/settings", h.Admin.ShareScheduling.GetSettings)
		share.PUT("/settings", h.Admin.ShareScheduling.UpdateSettings)
		share.GET("/groups", h.Admin.ShareScheduling.ListGroups)
		share.GET("/groups/:id/policy", h.Admin.ShareScheduling.GetPolicy)
		share.PUT("/groups/:id/policy", h.Admin.ShareScheduling.UpdatePolicy)
		share.POST("/groups/:id/preview", h.Admin.ShareScheduling.Preview)
		share.POST("/groups/:id/explain", h.Admin.ShareScheduling.Explain)
		share.GET("/groups/:id/report", h.Admin.ShareScheduling.Report)
		// Retired in the share scheduling simplification: 410 for old pages.
		share.GET("/groups/:id/decisions", adminhandler.RetiredShareSchedulingEndpoint)
		share.POST("/groups/:id/rebalance", adminhandler.RetiredShareSchedulingEndpoint)
		share.GET("/accounts/:id/rules", h.Admin.ShareScheduling.AccountRules)
	}
}
