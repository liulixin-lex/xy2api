package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	servermiddleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/stretchr/testify/require"
)

func TestClearAccountCapabilityRouteAdminProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: &adminhandler.AccountHandler{}, OpenAIOAuth: &adminhandler.OpenAIOAuthHandler{}}}
	audited := false
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		switch c.GetHeader("Authorization") {
		case "":
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
		case "Bearer user-token":
			servermiddleware.AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
		default:
			c.Set(string(servermiddleware.ContextKeyUserRole), "admin")
			c.Next()
		}
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { audited = true; c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil)
	for _, tc := range []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer user-token", http.StatusForbidden},
		{"Bearer admin-token", http.StatusBadRequest},
	} {
		t.Run(tc.auth, func(t *testing.T) {
			audited = false
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/accounts/not-an-id/capability-learning", nil)
			req.Header.Set("Authorization", tc.auth)
			router.ServeHTTP(w, req)
			require.Equal(t, tc.want, w.Code)
			if tc.want == http.StatusBadRequest {
				require.True(t, audited, "a permitted request must reach the audit middleware and account handler")
			}
		})
	}
}
