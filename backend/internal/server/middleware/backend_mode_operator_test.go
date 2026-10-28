//go:build unit

package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestBackendModeGuardAllowsOperator(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		for _, role := range []string{"admin", "operator", "user"} {
			svc := newBackendModeSettingService(t, enabled)
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set(string(ContextKeyUserRole), role) }, BackendModeUserGuard(svc))
			r.GET("/self", func(c *gin.Context) { c.Status(204) })
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest("GET", "/self", nil))
			want := 204
			if enabled == "true" && role == "user" {
				want = 403
			}
			require.Equal(t, want, rec.Code)
		}
	}
}
