package admin

import (
	"sort"

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
		AccountIDs []int64 `json:"account_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "account_ids must be an array of positive integers")
		return
	}
	seen := make(map[int64]struct{}, len(request.AccountIDs))
	ids := make([]int64, 0, len(request.AccountIDs))
	for _, id := range request.AccountIDs {
		if id <= 0 {
			response.BadRequest(c, "Account ids must be positive integers")
			return
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > service.RecentStatsMaxAccounts {
			response.BadRequest(c, "Too many account ids")
			return
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result, err := h.service.Batch(c.Request.Context(), ids)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, result)
}
