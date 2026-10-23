package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
)

func registerCachePolicyRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.CachePolicy == nil {
		return
	}
	policies := admin.Group("/cache/policies")
	p := h.Admin.CachePolicy
	policies.GET("", p.List)
	policies.GET("/history", p.History)
	// Literal routes preserve the per-key action derived by AuditLogMiddleware.
	for _, key := range []string{"master", "observability", "northstar", "upstream_identity", "anthropic_identity", "routing"} {
		policies.GET("/"+key, p.Get(key))
		policies.POST("/"+key+"/validate", p.Validate(key))
		policies.PUT("/"+key, p.Put(key))
	}
}
