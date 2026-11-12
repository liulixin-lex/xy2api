package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

func logAnthropicCompatSelectionError(c *gin.Context, log *zap.Logger, entry string, err error, status int, code string) {
	fields := []zap.Field{zap.String("entrypoint", entry), zap.Int("status", status),
		zap.String("code", code), zap.String("reason", string(service.AnthropicCompatErrorReason(err)))}
	if c != nil && c.Request != nil {
		ctx := c.Request.Context()
		accountID := service.AnthropicCompatBoundAccountID(ctx)
		if accountID == 0 {
			accountID, _ = ctx.Value(ctxkey.AccountID).(int64)
		}
		if accountID > 0 {
			fields = append(fields, zap.Int64("account_id", accountID))
		}
		if platform, _ := ctx.Value(ctxkey.Platform).(string); platform != "" {
			fields = append(fields, zap.String("account_platform", platform))
		}
	}
	log.Warn("gateway.anthropic_compat_selection_rejected", fields...)
}
