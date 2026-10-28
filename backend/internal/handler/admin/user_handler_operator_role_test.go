//go:build unit

package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestCreateOperatorRequiresStepUp(t *testing.T) {
	r, _ := setupRoleStepUpRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/v1/admin/users", map[string]any{"email": "operator@example.com", "password": "pass123", "role": "operator"})
	require.Equal(t, 401, rec.Code)
}
func TestChangingRoleToOrFromOperatorRequiresStepUp(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		status   int
	}{{"user", "operator", 401}, {"operator", "user", 401}, {"operator", "admin", 401}, {"admin", "operator", 401}, {"operator", "operator", 200}, {"user", "user", 200}} {
		t.Run(tc.from+"_"+tc.to, func(t *testing.T) {
			r, svc := setupRoleStepUpRouter(t)
			svc.users[0].Role = tc.from
			rec := doJSON(t, r, http.MethodPut, "/api/v1/admin/users/1", map[string]any{"role": tc.to})
			require.Equal(t, tc.status, rec.Code)
		})
	}
}
func TestCannotDemoteSelfToOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := newStubAdminService()
	h := NewUserHandler(svc, nil, nil, nil, nil, nil, nil)
	r.Use(func(c *gin.Context) { c.Set("user", middleware.AuthSubject{UserID: 1}) })
	r.PUT("/api/v1/admin/users/:id", h.Update)
	rec := doJSON(t, r, http.MethodPut, "/api/v1/admin/users/1", map[string]any{"role": service.RoleOperator})
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "cannot demote yourself")
}
