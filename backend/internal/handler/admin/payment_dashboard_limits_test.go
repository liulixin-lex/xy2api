package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPaymentDashboardDaysWithOrdersViewPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		query string
		days  int
	}{
		{"", 30}, {"?days=1", 1}, {"?days=7", 7}, {"?days=30", 30}, {"?days=90", 90},
		{"?days=", 0}, {"?days=0", 0}, {"?days=-1", 0}, {"?days=91", 0},
		{"?days=2147483647", 0}, {"?days=999999999999999999999999999", 0}, {"?days=abc", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			if tc.days > 0 {
				mock.ExpectQuery(`SELECT .* FROM "payment_orders"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectQuery(`SELECT COUNT\(.* FROM "payment_orders"`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			}
			h := NewPaymentHandler(service.NewPaymentService(client, nil, nil, nil, nil, nil, nil, nil, nil), nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				scope := &operatoraccess.Scope{UserID: 7, Perms: operatoraccess.Permissions{
					Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleOrders: operatoraccess.LevelView},
				}}
				c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), scope))
				c.Next()
			})
			const path = "/api/v1/admin/payment/dashboard"
			router.GET(path, middleware.AdminPermission(), h.GetDashboard)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+tc.query, nil))
			if tc.days == 0 {
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			} else {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var body struct {
					Data service.DashboardStats `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Len(t, body.Data.DailySeries, tc.days)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
