package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
	"net/http"
	"time"
)

func (h *AccountHandler) SetUpstreamBalanceService(svc *service.UpstreamBalanceService) {
	h.upstreamBalance = svc
}
func (h *AccountHandler) GetUpstreamBalances(c *gin.Context) {
	ids, err := parseAccountIDs(c)
	if err != nil {
		response.ErrorFrom(c, service.ErrUpstreamBalanceInvalidIDs)
		return
	}
	if len(ids) > service.UpstreamBalanceMaxReadIDs {
		response.ErrorFrom(c, service.ErrUpstreamBalanceTooManyIDs)
		return
	}
	if h.upstreamBalance == nil {
		response.ErrorFrom(c, service.ErrUpstreamBalanceUnavailable)
		return
	}
	items, err := h.upstreamBalance.ReadBalances(c.Request.Context(), ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "success", "data": gin.H{"items": items, "server_time": time.Now().UTC().Format(time.RFC3339)}})
}
