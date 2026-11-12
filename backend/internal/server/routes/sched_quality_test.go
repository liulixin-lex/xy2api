//go:build unit

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedQualityRouteAccounts struct {
	service.SchedAdminAccountRepository
	account *service.Account
}

func (r *schedQualityRouteAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.account.ID {
		return nil, service.ErrAccountNotFound
	}
	return r.account, nil
}

func (r *schedQualityRouteAccounts) SchedAdminClearModelLimit(_ context.Context, id int64, key string, original any) (bool, error) {
	limits := r.account.Extra["model_rate_limits"].(map[string]any)
	if id != r.account.ID || !reflect.DeepEqual(limits[key], original) {
		return false, nil
	}
	delete(limits, key)
	return true, nil
}

func TestSchedQualityClearAutoStateWithSlash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	limits := map[string]any{
		"vendor/model": map[string]any{"rate_limit_reset_at": now.Add(time.Hour).Format(time.RFC3339)},
		"other/model":  map[string]any{"rate_limit_reset_at": now.Add(time.Hour).Format(time.RFC3339)},
	}
	repo := &schedQualityRouteAccounts{account: &service.Account{ID: 1, Status: service.StatusActive, Schedulable: true, Extra: map[string]any{"model_rate_limits": limits}}}
	s := service.NewSchedAdminService(service.SchedAdminDependencies{Accounts: repo, Now: func() time.Time { return now }})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{"accounts": operatoraccess.LevelManage}}
		c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), &operatoraccess.Scope{UserID: 7, Perms: perms}))
		c.Next()
	}, middleware.AdminPermission())
	registerSchedQualityRoutes(router.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{SchedQuality: admin.NewSchedQualityHandler(s)}})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/sched-quality/accounts/1/auto-states/model_limit%3Avendor%2Fmodel", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"cleared":true`)
	require.NotContains(t, limits, "vendor/model")
	require.Contains(t, limits, "other/model")
	states, err := s.AutoStates(context.Background(), 1)
	require.NoError(t, err)
	for _, state := range states {
		require.NotEqual(t, "model_limit:vendor/model", state.Key)
	}
}
