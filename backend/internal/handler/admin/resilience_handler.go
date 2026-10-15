package admin

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// ResilienceHandler serves the gateway resilience administration endpoints.
type ResilienceHandler struct {
	svc *service.ResilienceService
}

// NewResilienceHandler creates the handler.
func NewResilienceHandler(svc *service.ResilienceService) *ResilienceHandler {
	return &ResilienceHandler{svc: svc}
}

// GetSettings returns the effective gateway resilience settings.
// GET /api/v1/admin/settings/gateway-resilience
func (h *ResilienceHandler) GetSettings(c *gin.Context) {
	response.Success(c, gin.H{"settings": h.svc.GetSettings(), "defaults": resilience.Defaults()})
}

// UpdateSettings replaces the settings. Omitted fields keep their current
// value; unknown fields are rejected.
// PUT /api/v1/admin/settings/gateway-resilience
func (h *ResilienceHandler) UpdateSettings(c *gin.Context) {
	next := h.svc.GetSettings()
	if !decodeStrictJSON(c, &next) {
		return
	}
	updated, err := h.svc.UpdateSettings(c.Request.Context(), next)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"settings": updated, "defaults": resilience.Defaults()})
}

// RecheckBalance probes an account's upstream balance immediately, for
// example after the administrator topped it up.
// POST /api/v1/admin/accounts/:id/balance/recheck
func (h *ResilienceHandler) RecheckBalance(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account id")
		return
	}
	rec, err := h.svc.RecheckBalance(c.Request.Context(), id)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, rec)
}
