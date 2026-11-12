//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
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
	filters  *service.BulkUpdateAccountFilters
}

func (s *schedQualityListAdmin) BulkUpdateAccounts(ctx context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	s.calls++
	s.ids, s.filtered = service.SchedQualityAccountFilter(ctx)
	s.filters = input.Filters
	return &service.BulkUpdateAccountsResult{}, nil
}

func (s *schedQualityListAdmin) ListAccounts(ctx context.Context, _ int, _ int, _, _, _, _ string, _ int64, _ string, _, _ string) ([]service.Account, int64, error) {
	s.calls++
	s.ids, s.filtered = service.SchedQualityAccountFilter(ctx)
	return []service.Account{}, 0, nil
}

type schedQualityAttention struct {
	SchedQualityAdmin
	cooling []int64
	err     error
}

func (s schedQualityAttention) Attention(context.Context) (service.SchedAdminAttention, error) {
	return service.SchedAdminAttention{Cooling: s.cooling}, s.err
}

func TestAccountSchedQualitySecondaryFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []struct {
		name, method, path string
		register           func(*gin.Engine, *AccountHandler)
	}{
		{"bulk", "POST", "/accounts/bulk-update", func(r *gin.Engine, h *AccountHandler) { r.POST("/accounts/bulk-update", h.BulkUpdate) }},
		{"export", "GET", "/accounts/data", func(r *gin.Engine, h *AccountHandler) { r.GET("/accounts/data", h.ExportData) }},
		{"billing", "GET", "/accounts/upstream-billing-rates", func(r *gin.Engine, h *AccountHandler) {
			r.GET("/accounts/upstream-billing-rates", h.GetUpstreamBillingRates)
		}},
	} {
		for _, tc := range []struct {
			name, quality string
			ids           []int64
			err           error
			unavailable   bool
			code          int
		}{
			{name: "cooling", quality: "cooling", ids: []int64{2, 4}, code: 200},
			{name: "empty_cooling", quality: "cooling", ids: []int64{}, code: 200},
			{name: "unfiltered", code: 200},
			{name: "invalid", quality: "normal", code: 400},
			{name: "unavailable", quality: "cooling", unavailable: true, code: 503},
			{name: "attention_error", quality: "cooling", err: errors.New("attention unavailable"), code: 500},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				admin := &schedQualityListAdmin{}
				h := &AccountHandler{adminService: admin}
				if !tc.unavailable {
					h.schedQuality = schedQualityAttention{cooling: tc.ids, err: tc.err}
				}
				r := gin.New()
				route.register(r, h)
				path, body := route.path+"?sched_quality="+tc.quality, ""
				if route.name == "bulk" {
					path = route.path
					body = `{"filters":{"sched_quality":"` + tc.quality + `"},"schedulable":true}`
				}
				req := httptest.NewRequest(route.method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, tc.code, w.Code, w.Body.String())
				if tc.code != 200 {
					require.Zero(t, admin.calls)
					return
				}
				require.Equal(t, 1, admin.calls)
				require.Equal(t, tc.quality == "cooling", admin.filtered)
				require.Equal(t, tc.ids, admin.ids)
				if route.name == "bulk" {
					require.Equal(t, tc.quality, admin.filters.SchedQuality)
				}
			})
		}
	}
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
