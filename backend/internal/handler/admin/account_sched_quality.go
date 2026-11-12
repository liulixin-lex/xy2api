package admin

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (h *AccountHandler) bindSchedQualityAccountFilter(c *gin.Context, quality string) bool {
	quality = strings.TrimSpace(quality)
	if quality == "" {
		return true
	}
	if quality != "cooling" {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_SCHED_QUALITY", "invalid sched_quality filter"))
		return false
	}
	if h.schedQuality == nil {
		response.Error(c, http.StatusServiceUnavailable, "Scheduling quality service unavailable")
		return false
	}
	attention, err := h.schedQuality.Attention(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return false
	}
	c.Request = c.Request.WithContext(service.WithSchedQualityAccountFilter(c.Request.Context(), attention.Cooling))
	return true
}
