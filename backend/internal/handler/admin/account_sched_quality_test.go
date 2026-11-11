//go:build unit

package admin

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedQualityListAdmin struct {
	service.AdminService
	ids      []int64
	filtered bool
	calls    int
}

func (s *schedQualityListAdmin) ListAccounts(ctx context.Context, _ int, _ int, _, _, _, _ string, _ int64, _ string, _, _ string) ([]service.Account, int64, error) {
	s.calls++
	s.ids, s.filtered = service.SchedQualityAccountFilter(ctx)
	return []service.Account{}, 0, nil
}

type schedQualityAttention struct {
	SchedQualityAdmin
	cooling []int64
}

func (s schedQualityAttention) Attention(context.Context) (service.SchedAdminAttention, error) {
	return service.SchedAdminAttention{Cooling: s.cooling}, nil
}
func TestAccountSchedQualityCoolingFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ids := range [][]int64{{2, 4}, {}} {
		t.Run(string(rune('0'+len(ids))), func(t *testing.T) {
			admin := &schedQualityListAdmin{}
			h := &AccountHandler{adminService: admin, schedQuality: schedQualityAttention{cooling: ids}}
			r := gin.New()
			r.GET("/accounts", h.List)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/accounts?sched_quality=cooling", nil))
			require.Equal(t, 200, w.Code, w.Body.String())
			require.True(t, admin.filtered)
			require.Equal(t, ids, admin.ids)
			require.Equal(t, 1, admin.calls)
		})
	}
	admin := &schedQualityListAdmin{}
	r := gin.New()
	r.GET("/accounts", (&AccountHandler{adminService: admin}).List)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/accounts?sched_quality=normal", nil))
	require.Equal(t, 400, w.Code)
	require.Zero(t, admin.calls)
}
func TestAccountRecoverStateUsesQualityReset(t *testing.T) {
	h, _, _ := schedAdminHandlerFixture()
	account := &AccountHandler{schedQuality: h.service}
	r := gin.New()
	r.POST("/accounts/:id/recover-state", account.RecoverState)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/accounts/1/recover-state", nil))
	data := schedAdminHandlerData(t, w)
	for _, key := range []string{"reset_at", "cleared", "preserved", "failed"} {
		require.Contains(t, data, key)
	}
	require.NotContains(t, data, "credentials")
}
