package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAITOTPRoutesEnforceFullAdminAndAlwaysStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &handler.Handlers{Admin: &handler.AdminHandlers{OpenAITOTP: admin.NewOpenAITOTPHandler(nil, nil, nil, nil, &config.Config{})}}
	for _, role := range []string{service.RoleAdmin, service.RoleOperator} {
		r := gin.New()
		group := r.Group("/api/v1/admin", func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
		registerOpenAITOTPRoutes(group, h)
		for _, request := range []struct{ method, path string }{{"PUT", "/accounts/1/openai-totp"}, {"POST", "/accounts/1/openai-totp/rotate"}, {"POST", "/accounts/1/openai-totp/reconcile"}, {"POST", "/accounts/1/openai-totp/export"}, {"POST", "/accounts/openai-login"}, {"POST", "/accounts/openai-login/1/import"}} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(request.method, "/api/v1/admin"+request.path, strings.NewReader(`{}`)))
			require.Equal(t, http.StatusForbidden, w.Code, request.path)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		}
	}
}
func TestOpenAITOTPWorkerRoutesUseIndependentBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{OpenAITOTP: admin.NewOpenAITOTPHandler(nil, nil, nil, nil, &config.Config{OpenAITOTP: config.OpenAITOTPConfig{WorkerSecret: strings.Repeat("w", 32)}})}}
	audited := false
	RegisterOpenAITOTPWorkerRoutes(r.Group("/api/v1"), h, middleware.AuditLogMiddleware(func(c *gin.Context) { audited = true; c.Next() }))
	for _, path := range []string{"/claim", "/1/phase", "/1/finish", "/recover"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/internal/openai-totp"+path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer ordinary-admin-token")
		r.ServeHTTP(w, req)
		require.Equal(t, 401, w.Code)
		require.False(t, audited)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/internal/openai-totp/claim", strings.NewReader(`{`))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("w", 32))
	r.ServeHTTP(w, req)
	require.Equal(t, 400, w.Code)
	require.True(t, audited)
}
