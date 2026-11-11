package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
)

func registerEmailCampaignRoutes(v1, admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth middleware.StepUpAuthMiddleware, panelRateLimiter *middleware.PanelRateLimiter) {
	campaigns := admin.Group("/email-campaigns")
	campaigns.GET("/settings", h.EmailCampaign.Settings)
	campaigns.PUT("/settings", h.EmailCampaign.UpdateSettings)
	campaigns.POST("/preview", h.EmailCampaign.Preview)
	campaigns.POST("/render", h.EmailCampaign.Render)
	campaigns.POST("/test-send", h.EmailCampaign.TestSend)
	campaigns.POST("", gin.HandlerFunc(stepUpAuth), h.EmailCampaign.Create)
	campaigns.GET("", h.EmailCampaign.List)
	campaigns.GET("/:id", h.EmailCampaign.Detail)
	campaigns.GET("/:id/recipients", h.EmailCampaign.Recipients)
	campaigns.POST("/:id/pause", h.EmailCampaign.Pause)
	campaigns.POST("/:id/resume", h.EmailCampaign.Resume)
	campaigns.POST("/:id/cancel", h.EmailCampaign.Cancel)
	campaigns.POST("/:id/retry-failed", h.EmailCampaign.RetryFailed)
	campaigns.GET("/suppressions", h.EmailCampaign.Suppressions)
	campaigns.POST("/suppressions", h.EmailCampaign.AddSuppressions)
	campaigns.DELETE("/suppressions/:id", h.EmailCampaign.RemoveSuppression)
	RegisterEmailCampaignUnsubscribeRoutes(v1, h.EmailCampaign, panelRateLimiter)
}

// RegisterEmailCampaignUnsubscribeRoutes is public: the only route-specific middleware is PublicIP.
func RegisterEmailCampaignUnsubscribeRoutes(v1 *gin.RouterGroup, h *handler.EmailCampaignHandler, panelRateLimiter *middleware.PanelRateLimiter) {
	v1.GET("/email-campaigns/unsubscribe", panelRateLimiter.PublicIP(), h.Unsubscribe)
	v1.POST("/email-campaigns/unsubscribe", panelRateLimiter.PublicIP(), h.Unsubscribe)
}
