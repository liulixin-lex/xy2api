package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
)

func registerSchedQualityRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	g := admin.Group("/sched-quality")
	q := h.Admin.SchedQuality
	g.POST("/accounts/summary", q.Summary)
	g.GET("/accounts/attention", q.Attention)
	g.GET("/accounts/:id", q.Detail)
	g.POST("/accounts/:id/reset", q.Reset)
	g.PUT("/accounts/:id/override", q.SetOverride)
	g.DELETE("/accounts/:id/override", q.ClearOverride)
	g.DELETE("/accounts/:id/auto-states/*key", q.ClearAutoState)
	g.GET("/groups/:id/policy", q.Policy)
	g.PUT("/groups/:id/policy", q.UpdatePolicy)
	g.GET("/groups/:id/order", q.Order)
	g.POST("/groups/summary", q.GroupsSummary)
	g.POST("/groups/:id/policy/preview", q.PreviewPolicy)
}
