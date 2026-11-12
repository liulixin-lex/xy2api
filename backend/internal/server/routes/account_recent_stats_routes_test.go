//go:build unit

package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

type recentStatsRouteRepository struct {
	ids []int64
}

func (r *recentStatsRouteRepository) Batch(_ context.Context, ids []int64, _ time.Time, _ bool) ([]service.AccountRecentStatsRow, error) {
	r.ids = ids
	return []service.AccountRecentStatsRow{{AccountID: 7, ErrorN: 100, ErrorBad: 2}}, nil
}

func TestAccountRecentStatsRouteOperatorPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		module operatoraccess.Module
		level  operatoraccess.Level
		status int
	}{
		{"accounts view", "accounts", operatoraccess.LevelView, http.StatusOK},
		{"channels view only", "channels", operatoraccess.LevelView, http.StatusForbidden},
		{"groups view only", "groups", operatoraccess.LevelView, http.StatusForbidden},
		{"usage view only", "usage", operatoraccess.LevelView, http.StatusForbidden},
		{"no accounts view", "accounts", operatoraccess.LevelNone, http.StatusForbidden},
		{"ops view only", "ops", operatoraccess.LevelView, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recentStatsRouteRepository{}
			stats := service.NewAccountRecentStatsService(repo, &service.OpsService{})
			h := &handler.Handlers{Admin: &handler.AdminHandlers{
				AccountRecentStats: admin.NewAccountRecentStatsHandler(stats),
			}}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{tc.module: tc.level}}
				c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), &operatoraccess.Scope{UserID: 7, Perms: perms}))
				c.Next()
			}, middleware.AdminPermission())
			registerAccountRoutes(router.Group("/api/v1/admin"), h, func(c *gin.Context) { c.Next() })
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/recent-stats/batch", strings.NewReader(`{"account_ids":[7]}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == http.StatusForbidden {
				require.Contains(t, w.Body.String(), "ADMIN_PERMISSION_DENIED")
				require.Nil(t, repo.ids)
				return
			}
			require.Equal(t, []int64{7}, repo.ids)
			var envelope struct {
				Data service.AccountRecentStats `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
			require.Len(t, envelope.Data.Items, 1)
			item := envelope.Data.Items[0]
			require.EqualValues(t, 7, item.AccountID)
			require.NotNil(t, item.Error)
			require.NotNil(t, item.Error.Rate)
			require.Equal(t, .02, *item.Error.Rate)
		})
	}
}
