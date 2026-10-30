package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
)

func registerOpenAITOTPRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.OpenAITOTP == nil {
		return
	}
	x := h.Admin.OpenAITOTP
	accounts := admin.Group("/accounts", x.AdminGuard)
	accounts.GET("/:id/openai-totp", x.Status)
	accounts.PUT("/:id/openai-totp", x.StepUp, x.Save)
	accounts.POST("/:id/openai-totp/rotate", x.StepUp, x.Rotate)
	accounts.POST("/:id/openai-totp/reconcile", x.StepUp, x.Reconcile)
	accounts.POST("/:id/openai-totp/export", x.StepUp, x.Export)
	accounts.POST("/openai-login", x.StepUp, x.Login)
	accounts.GET("/openai-login/:task_id", x.LoginTask)
	accounts.POST("/openai-login/:task_id/import", x.StepUp, x.Import)
}
func RegisterOpenAITOTPWorkerRoutes(v1 *gin.RouterGroup, h *handler.Handlers, audit middleware.AuditLogMiddleware) {
	if h == nil || h.Admin == nil || h.Admin.OpenAITOTP == nil {
		return
	}
	x := h.Admin.OpenAITOTP
	worker := v1.Group("/internal/openai-totp", x.WorkerGuard, gin.HandlerFunc(audit))
	worker.POST("/claim", x.Claim)
	worker.POST("/:task_id/phase", x.Phase)
	worker.POST("/:task_id/finish", x.Finish)
	worker.POST("/recover", x.Recover)
}
