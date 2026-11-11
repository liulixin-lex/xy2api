package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// ProfitPreviewHandler serves the group profit preview API.
type ProfitPreviewHandler struct {
	svc *service.ProfitPreviewService
}

func NewProfitPreviewHandler(svc *service.ProfitPreviewService) *ProfitPreviewHandler {
	return &ProfitPreviewHandler{svc: svc}
}

func (h *ProfitPreviewHandler) ProfitControlPreview(c *gin.Context) {
	if h == nil || h.svc == nil {
		response.Error(c, http.StatusServiceUnavailable, "group quality gate unavailable")
		return
	}
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var margin *float64
	if raw, ok := c.GetQuery("margin"); ok && raw != "" {
		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			response.BadRequest(c, "Invalid margin")
			return
		}
		margin = &value
	}
	out, err := h.svc.ProfitControlPreview(c.Request.Context(), groupID, margin)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}
