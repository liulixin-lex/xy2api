package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// setOperatorScope binds freshly loaded permissions to both contexts, failing closed.
func setOperatorScope(c *gin.Context, user *service.User, access *service.OperatorAccessService) bool {
	if user == nil {
		return true
	}
	if user.Role == service.RoleAdmin {
		setSuperAdminActor(c, user.ID)
		return true
	}
	if user.Role != service.RoleOperator {
		return true
	}
	if access != nil {
		scope, err := access.LoadScope(c.Request.Context(), user)
		if err == nil && scope != nil {
			c.Set(operatoraccess.GinKey, scope)
			c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), scope))
			return true
		}
	}
	AbortWithError(c, http.StatusServiceUnavailable, "OPERATOR_ACCESS_UNAVAILABLE", "Operator permissions are unavailable")
	return false
}

func setSuperAdminActor(c *gin.Context, userID int64) {
	c.Request = c.Request.WithContext(operatoraccess.WithSuperAdmin(c.Request.Context(), userID))
}
