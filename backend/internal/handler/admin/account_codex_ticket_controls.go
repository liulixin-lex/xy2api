package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type codexAccountTicketManager interface {
	GetCodexAccountTicketStatus(context.Context, int64) (*service.CodexAccountTicketStatus, error)
	ConfigureCodexAccountTicket(context.Context, int64, service.CodexAccountTicketUpdate) (*service.CodexAccountTicketStatus, error)
	HarvestCodexAccountTicket(context.Context, int64) (*service.CodexAccountTicketStatus, error)
}

func (h *AccountHandler) SetCodexAccountTicketService(s *service.OpenAIGatewayService) {
	h.codexAccountTickets = s
}

func (h *AccountHandler) codexTicketAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	if h.codexAccountTickets == nil {
		response.Error(c, http.StatusServiceUnavailable, "Account STATE ticket service unavailable")
		return 0, false
	}
	return id, true
}

// Only explicit, public application errors may be returned; never echo a proxy
// URL from a JSON binding, database, or transport error into the admin response.
func codexTicketControlError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if infraerrors.Code(err) >= http.StatusInternalServerError {
		response.InternalError(c, "Account STATE ticket operation failed")
	} else {
		response.ErrorFrom(c, err)
	}
	return true
}

func (h *AccountHandler) GetCodexAccountTicket(c *gin.Context) {
	id, ok := h.codexTicketAccountID(c)
	if !ok {
		return
	}
	status, err := h.codexAccountTickets.GetCodexAccountTicketStatus(c.Request.Context(), id)
	if !codexTicketControlError(c, err) {
		response.Success(c, status)
	}
}

func (h *AccountHandler) UpdateCodexAccountTicket(c *gin.Context) {
	id, ok := h.codexTicketAccountID(c)
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision string   `json:"expected_revision"`
		Models           []string `json:"models"`
		MissingPolicy    string   `json:"missing_policy"`
		TicketPlan       string   `json:"ticket_plan"`
		Enabled          *bool    `json:"enabled"`
		ProxyURL         string   `json:"proxy_url"`
		Model            string   `json:"model"`
		ClearProxy       bool     `json:"clear_proxy"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&req); err != nil || (c.Request.Method == http.MethodPut && req.Enabled == nil) || (c.Request.Method == http.MethodPatch && req.ExpectedRevision == "") {
		response.BadRequest(c, "Invalid STATE settings; PATCH requires expected_revision and PUT requires enabled")
		return
	}
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	status, err := h.codexAccountTickets.ConfigureCodexAccountTicket(c.Request.Context(), id, service.CodexAccountTicketUpdate{
		ExpectedRevision: req.ExpectedRevision, RequireRevision: c.Request.Method == http.MethodPatch, PreserveEnabled: req.Enabled == nil, GuardEnable: true,
		Models: req.Models, MissingPolicy: req.MissingPolicy, TicketPlan: req.TicketPlan, Enabled: enabled, ProxyURL: req.ProxyURL, Model: req.Model, ClearProxy: req.ClearProxy,
	})
	if !codexTicketControlError(c, err) {
		response.Success(c, status)
	}
}

func (h *AccountHandler) HarvestCodexAccountTicket(c *gin.Context) {
	id, ok := h.codexTicketAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Model     string `json:"model"`
		ProxyID   string `json:"proxy_id"`
		RequestID string `json:"request_id"`
	}
	if c.Request.ContentLength != 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
		if c.ShouldBindJSON(&input) != nil {
			response.BadRequest(c, "Invalid STATE harvest request")
			return
		}
	}
	var status *service.CodexAccountTicketStatus
	var err error
	if manager, ok := h.codexAccountTickets.(interface {
		HarvestCodexAccountTicketOptions(context.Context, int64, string, string, string) (*service.CodexAccountTicketStatus, error)
	}); ok {
		status, err = manager.HarvestCodexAccountTicketOptions(c.Request.Context(), id, input.Model, input.ProxyID, input.RequestID)
	} else if manager, ok := h.codexAccountTickets.(interface {
		HarvestCodexAccountTicketModel(context.Context, int64, string) (*service.CodexAccountTicketStatus, error)
	}); ok {
		status, err = manager.HarvestCodexAccountTicketModel(c.Request.Context(), id, input.Model)
	} else {
		status, err = h.codexAccountTickets.HarvestCodexAccountTicket(c.Request.Context(), id)
	}
	if !codexTicketControlError(c, err) {
		response.Accepted(c, status)
	}
}

func (h *AccountHandler) GetCodexAccountTicketDiagnostics(c *gin.Context) {
	id, ok := h.codexTicketAccountID(c)
	if !ok {
		return
	}
	status, err := h.codexAccountTickets.GetCodexAccountTicketStatus(c.Request.Context(), id)
	if !codexTicketControlError(c, err) {
		response.Success(c, gin.H{"version": 1, "tickets": status.Tickets})
	}
}

func (h *AccountHandler) GetCodexTicketEvents(c *gin.Context) {
	id, ok := h.codexTicketAccountID(c)
	if !ok {
		return
	}
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	manager, ok := h.codexAccountTickets.(interface {
		GetCodexTicketEvents(context.Context, int64, string, int64) ([]service.CodexTicketTraceEvent, error)
	})
	if !ok {
		response.Error(c, http.StatusServiceUnavailable, "STATE event service unavailable")
		return
	}
	events, err := manager.GetCodexTicketEvents(c.Request.Context(), id, c.Query("model"), before)
	if !codexTicketControlError(c, err) {
		response.Success(c, gin.H{"events": events})
	}
}
