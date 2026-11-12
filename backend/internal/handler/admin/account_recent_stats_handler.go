package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type AccountRecentStatsHandler struct {
	service *service.AccountRecentStatsService
}

func NewAccountRecentStatsHandler(s *service.AccountRecentStatsService) *AccountRecentStatsHandler {
	return &AccountRecentStatsHandler{service: s}
}

func (h *AccountRecentStatsHandler) Batch(c *gin.Context) {
	middleware.SkipAudit(c)
	var request struct {
		AccountIDs []int64 `json:"account_ids" binding:"required,dive,gt=0"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "account_ids must be an array of positive integers")
		return
	}
	ids, err := service.NormalizeRecentStatsAccountIDs(request.AccountIDs)
	if response.ErrorFrom(c, err) {
		return
	}
	result, err := h.service.Batch(c.Request.Context(), ids)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, result)
}
