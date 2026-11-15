//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type managementMCPSettingsRepo struct {
	service.SettingRepository
	values map[string]string
}

func (r *managementMCPSettingsRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return r.values, nil
}

func (r *managementMCPSettingsRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func TestManagementMCPSettingsRequireSuperAdminBrowserSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ role, method string }{
		{service.RoleUser, service.AuditAuthMethodJWT},
		{"operator", service.AuditAuthMethodJWT},
		{service.RoleAdmin, "admin_api_key"},
		{service.RoleAdmin, service.AuditAuthMethodMCPOAuth},
		{service.RoleAdmin, "scoped_admin_key"},
		{service.RoleAdmin, service.AuditAuthMethodJWT},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			repo := &managementMCPSettingsRepo{values: map[string]string{}}
			h := &SettingHandler{settingService: service.NewSettingService(repo, &config.Config{})}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUserRole), tc.role)
				c.Set("auth_method", tc.method)
			})
			r.GET("/settings", h.GetManagementMCPSettings)
			r.PUT("/settings", h.UpdateManagementMCPSettings)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/settings", strings.NewReader(`{"enabled":true,"public_url":"https://api.example/api/v1/mcp"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if tc.role == service.RoleAdmin && tc.method == service.AuditAuthMethodJWT {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			} else {
				require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				require.Empty(t, repo.values)
			}
		}
	}
}
