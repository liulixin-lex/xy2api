package admin

import (
	"io"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// OperatorAccessHandler serves the console bootstrap and super-admin grants.
type OperatorAccessHandler struct {
	access   *service.OperatorAccessService
	totp     *service.TotpService
	users    *service.UserService
	settings *service.SettingService
}

func NewOperatorAccessHandler(access *service.OperatorAccessService, totp *service.TotpService, users *service.UserService, settings *service.SettingService) *OperatorAccessHandler {
	return &OperatorAccessHandler{access: access, totp: totp, users: users, settings: settings}
}

func (h *OperatorAccessHandler) MeAccess(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Authentication required")
		return
	}
	out, err := h.access.MeAccess(c.Request.Context(), &service.User{ID: subject.UserID, Role: c.GetString(string(middleware.ContextKeyUserRole))})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if out.IsSuperAdmin && h.settings != nil {
		response.Success(c, struct {
			service.MeAccess
			CustomMenuItems any `json:"custom_menu_items"`
		}{out, dto.ParseCustomMenuItems(h.settings.GetCustomMenuItemsRaw(c.Request.Context()))})
		return
	}
	response.Success(c, out)
}

func operatorPermissionsID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return 0, false
	}
	return id, true
}

func (h *OperatorAccessHandler) GetPermissions(c *gin.Context) {
	id, ok := operatorPermissionsID(c)
	if !ok {
		return
	}
	out, err := h.access.GetPermissions(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *OperatorAccessHandler) PutPermissions(c *gin.Context) {
	id, ok := operatorPermissionsID(c)
	if !ok {
		return
	}
	if !middleware.EnforceStepUp(c, h.totp, h.users, h.settings) {
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	out, err := h.access.PutPermissions(c.Request.Context(), subject.UserID, id, raw)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
