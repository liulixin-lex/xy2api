package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func requireManagementMCPAdmin(c *gin.Context) bool {
	role, _ := middleware.GetUserRoleFromContext(c)
	if role != service.RoleAdmin || c.GetString("auth_method") != service.AuditAuthMethodJWT {
		response.Forbidden(c, "Only a super administrator browser session can configure management MCP")
		return false
	}
	return true
}

func (h *SettingHandler) GetManagementMCPSettings(c *gin.Context) {
	if !requireManagementMCPAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	settings, err := h.settingService.GetManagementMCPSettings(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateManagementMCPSettings(c *gin.Context) {
	if !requireManagementMCPAdmin(c) {
		return
	}
	var input service.ManagementMCPSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid management MCP settings")
		return
	}
	settings, err := h.settingService.SetManagementMCPSettings(c.Request.Context(), input)
	if response.ErrorFrom(c, err) {
		return
	}
	middleware.SetAuditAction(c, "admin.management_mcp.configure")
	response.Success(c, settings)
}
