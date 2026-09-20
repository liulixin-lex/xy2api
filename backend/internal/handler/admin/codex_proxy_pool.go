package admin

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type codexProxyManager interface {
	GetCodexHarvestProxies(context.Context) (*service.CodexHarvestProxyPool, error)
	SaveCodexHarvestProxies(context.Context, service.CodexHarvestProxyPoolUpdate) (*service.CodexHarvestProxyPool, error)
	TestCodexHarvestProxy(context.Context, service.CodexProxyTestInput) (*service.CodexProxyProbeResult, error)
}

func (h *AccountHandler) codexProxyManager(c *gin.Context) codexProxyManager {
	manager, ok := h.codexAccountTickets.(codexProxyManager)
	if !ok {
		response.Error(c, http.StatusServiceUnavailable, "STATE proxy service unavailable")
		return nil
	}
	return manager
}
func (h *AccountHandler) GetCodexHarvestProxies(c *gin.Context) {
	m := h.codexProxyManager(c)
	if m == nil {
		return
	}
	pool, err := m.GetCodexHarvestProxies(c.Request.Context())
	if !codexTicketControlError(c, err) {
		response.Success(c, pool)
	}
}
func (h *AccountHandler) SaveCodexHarvestProxies(c *gin.Context) {
	m := h.codexProxyManager(c)
	if m == nil {
		return
	}
	var input service.CodexHarvestProxyPoolUpdate
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024)
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid proxy pool")
		return
	}
	pool, err := m.SaveCodexHarvestProxies(c.Request.Context(), input)
	if !codexTicketControlError(c, err) {
		response.Success(c, pool)
	}
}
func (h *AccountHandler) TestCodexHarvestProxy(c *gin.Context) {
	m := h.codexProxyManager(c)
	if m == nil {
		return
	}
	var input service.CodexProxyTestInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid proxy test")
		return
	}
	result, err := m.TestCodexHarvestProxy(c.Request.Context(), input)
	if !codexTicketControlError(c, err) {
		response.Success(c, result)
	}
}
