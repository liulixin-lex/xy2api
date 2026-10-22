package routes

import (
	"github.com/liulixin-lex/xy2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerGroupCategoryRoutes serves the admin-only group categories API.
func registerGroupCategoryRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.Admin == nil || h.Admin.GroupCategory == nil {
		return
	}
	categories := admin.Group("/group-categories")
	{
		categories.GET("", h.Admin.GroupCategory.List)
		categories.PUT("", h.Admin.GroupCategory.Save)
		categories.PUT("/groups/:group_id", h.Admin.GroupCategory.SetGroupCategories)
	}
}
