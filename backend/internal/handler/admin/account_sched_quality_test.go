//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedQualityListAdmin struct {
	service.AdminService
	filter   service.SchedQualityFilter
	filtered bool
	calls    int
	filters  *service.BulkUpdateAccountFilters
	matched  []int64
}

func (s *schedQualityListAdmin) selectAccounts(ctx context.Context, group int64) []service.Account {
	s.calls++
	s.filter, s.filtered = service.SchedQualityAccountFilter(ctx)
	s.matched = []int64{}
	rows := []service.Account{}
	for id := int64(1); id <= 6; id++ {
		member := id%2 == 0
		if group > 0 && !member {
			continue
		}
		if s.filtered && slices.Contains(s.filter.IDs, id) == s.filter.Exclude {
			continue
		}
		s.matched = append(s.matched, id)
		rows = append(rows, service.Account{ID: id, Name: strconv.FormatInt(id, 10), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	}
	return rows
}
func (s *schedQualityListAdmin) ListIQTargetSummaries(context.Context, []int64) (map[int64][]domain.IQTargetSummary, error) {
	return map[int64][]domain.IQTargetSummary{}, nil
}
func (s *schedQualityListAdmin) BulkUpdateAccounts(ctx context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	s.filters = input.Filters
	group, _ := strconv.ParseInt(input.Filters.Group, 10, 64)
	s.selectAccounts(ctx, group)
	return &service.BulkUpdateAccountsResult{SuccessIDs: s.matched}, nil
}
func (s *schedQualityListAdmin) ListAccounts(ctx context.Context, _, _ int, _, _, _, _ string, group int64, _ string, _, _ string) ([]service.Account, int64, error) {
	rows := s.selectAccounts(ctx, group)
	return rows, int64(len(rows)), nil
}

type schedQualityAttention struct {
	SchedQualityAdmin
	value service.SchedAdminAttention
	err   error
}

func (s schedQualityAttention) Attention(context.Context) (service.SchedAdminAttention, error) {
	return s.value, s.err
}

func TestAccountSchedQualityFiveFilterPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	attention := service.SchedAdminAttention{Substandard: []int64{1, 2}, Exempt: []int64{2, 3}, Observing: []int64{4}}
	for _, route := range []struct {
		name, method, path, query string
		register                  func(*gin.Engine, *AccountHandler)
	}{
		{"list", "GET", "/accounts", "", func(r *gin.Engine, h *AccountHandler) { r.GET("/accounts", h.List) }},
		{"select_all", "GET", "/accounts", "&lite=1&page=1&page_size=1000", func(r *gin.Engine, h *AccountHandler) { r.GET("/accounts", h.List) }},
		{"bulk", "POST", "/accounts/bulk-update", "", func(r *gin.Engine, h *AccountHandler) { r.POST("/accounts/bulk-update", h.BulkUpdate) }},
		{"export", "GET", "/accounts/data", "", func(r *gin.Engine, h *AccountHandler) { r.GET("/accounts/data", h.ExportData) }},
		{"billing", "GET", "/accounts/upstream-billing-rates", "", func(r *gin.Engine, h *AccountHandler) {
			r.GET("/accounts/upstream-billing-rates", h.GetUpstreamBillingRates)
		}},
	} {
		for _, tc := range []struct {
			name, quality, group string
			empty, unavailable   bool
			err                  error
			code                 int
			want                 []int64
		}{
			{name: "substandard", quality: "substandard", code: 200, want: []int64{1, 2}},
			{name: "exempt", quality: "exempt", code: 200, want: []int64{2, 3}},
			{name: "observing", quality: "observing", code: 200, want: []int64{4}},
			{name: "normal", quality: "normal", code: 200, want: []int64{5, 6}},
			{name: "exempt_group", quality: "exempt", group: "1", code: 200, want: []int64{2}},
			{name: "observing_group", quality: "observing", group: "1", code: 200, want: []int64{4}},
			{name: "normal_group", quality: "normal", group: "1", code: 200, want: []int64{6}},
			{name: "empty_substandard", quality: "substandard", empty: true, code: 200, want: []int64{}},
			{name: "empty_exempt", quality: "exempt", empty: true, code: 200, want: []int64{}},
			{name: "empty_observing", quality: "observing", empty: true, code: 200, want: []int64{}},
			{name: "normal_empty_union", quality: "normal", empty: true, code: 200, want: []int64{1, 2, 3, 4, 5, 6}},
			{name: "unfiltered", code: 200, want: []int64{1, 2, 3, 4, 5, 6}},
			{name: "invalid", quality: "unknown", code: 400},
			{name: "unavailable", quality: "normal", unavailable: true, code: 503},
			{name: "attention_error", quality: "exempt", err: errors.New("attention unavailable"), code: 500},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				admin := &schedQualityListAdmin{}
				h := &AccountHandler{adminService: admin}
				value := attention
				if tc.empty {
					value = service.SchedAdminAttention{}
				}
				if !tc.unavailable {
					h.schedQuality = schedQualityAttention{value: value, err: tc.err}
				}
				r := gin.New()
				route.register(r, h)
				path, body := route.path+"?sched_quality="+tc.quality+"&group="+tc.group+route.query, ""
				if route.name == "bulk" {
					path = route.path
					body = `{"filters":{"sched_quality":"` + tc.quality + `","group":"` + tc.group + `"},"schedulable":true}`
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
				require.Equal(t, tc.quality != "", admin.filtered)
				require.Equal(t, tc.quality == "normal", admin.filter.Exclude)
				require.Equal(t, tc.want, admin.matched)
				if tc.quality == "normal" && !tc.empty {
					require.Equal(t, []int64{1, 2, 3, 4}, admin.filter.IDs)
				}
				if route.name == "bulk" {
					require.Equal(t, tc.quality, admin.filters.SchedQuality)
				}
			})
		}
	}
}
func TestAccountRecoverStateUsesQualityReset(t *testing.T) {
	h, _, _ := schedAdminHandlerFixture()
	account := &AccountHandler{schedQuality: h.service}
	r := gin.New()
	r.POST("/accounts/:id/recover-state", account.RecoverState)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/accounts/1/recover-state", nil))
	data := schedAdminHandlerData(t, w)
	for _, key := range []string{"account_id", "reset_at", "cleared", "kept", "failed"} {
		require.Contains(t, data, key)
	}
	require.JSONEq(t, `["quality","session_avoid"]`, string(data["cleared"]))
	require.JSONEq(t, `[]`, string(data["kept"]))
	require.JSONEq(t, `[]`, string(data["failed"]))
	require.NotContains(t, data, "preserved")
	require.NotContains(t, data, "credentials")
}
