package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestParseAccountListIDs(t *testing.T) {
	boundary := make([]string, 1000)
	for i := range boundary {
		boundary[i] = strconv.Itoa(i + 1)
	}
	for _, tc := range []struct {
		query   string
		want    []int64
		invalid bool
	}{
		{"", nil, false}, {"ids=2,1&ids=2,3", []int64{2, 1, 3}, false},
		{"ids=%201%20,2", []int64{1, 2}, false},
		{"ids=", nil, true}, {"ids=1,", nil, true}, {"ids=1,,2", nil, true},
		{"ids=0", nil, true}, {"ids=-1", nil, true}, {"ids=x", nil, true},
		{"ids=1.5", nil, true}, {"ids=9223372036854775808", nil, true},
		{"ids=" + strings.Join(boundary, ",") + ",1001", nil, true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/accounts?"+tc.query, nil)
			ids, err := parseAccountListIDs(c)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, ids)
		})
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/accounts?ids="+strings.Join(boundary, ",")+"&ids=1", nil)
	ids, err := parseAccountListIDs(c)
	require.NoError(t, err)
	require.Len(t, ids, 1000)
}

type accountIDsListAdmin struct {
	*stubAdminService
	ids     []int64
	quality service.SchedQualityFilter
}

func (s *accountIDsListAdmin) ListAccounts(ctx context.Context, page, size int, platform, kind, status, search string, group int64, privacy, sortBy, sortOrder string) ([]service.Account, int64, error) {
	s.ids, _ = service.AccountIDsFilter(ctx)
	s.quality, _ = service.SchedQualityAccountFilter(ctx)
	return s.stubAdminService.ListAccounts(ctx, page, size, platform, kind, status, search, group, privacy, sortBy, sortOrder)
}

type accountIDsAttention struct{ SchedQualityAdmin }

func (accountIDsAttention) Attention(context.Context) (service.SchedAdminAttention, error) {
	return service.SchedAdminAttention{Exempt: []int64{2, 3}}, nil
}

func TestAccountIDsListPreservesQualityLiteAndETag(t *testing.T) {
	for _, lite := range []string{"0", "1"} {
		t.Run(lite, func(t *testing.T) {
			svc := &accountIDsListAdmin{stubAdminService: newStubAdminService()}
			svc.accounts = []service.Account{{ID: 2, Name: "selected", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}}
			h := &AccountHandler{adminService: svc, schedQuality: accountIDsAttention{}}
			r := gin.New()
			r.GET("/accounts", h.List)
			url := "/accounts?ids=1,2&ids=2&lite=" + lite + "&sched_quality=exempt"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, []int64{1, 2}, svc.ids)
			require.Equal(t, []int64{2, 3}, svc.quality.IDs)
			require.False(t, svc.quality.Exclude)
			require.NotEmpty(t, w.Header().Get("ETag"))
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("If-None-Match", w.Header().Get("ETag"))
			cached := httptest.NewRecorder()
			r.ServeHTTP(cached, req)
			require.Equal(t, http.StatusNotModified, cached.Code)
		})
	}
}

func TestAccountIDsListRejectsInvalidInputBeforeQuery(t *testing.T) {
	h := &AccountHandler{}
	r := gin.New()
	r.GET("/accounts", h.List)
	for _, query := range []string{"ids=nope", "ids=0", "ids="} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/accounts?"+query, nil))
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_ACCOUNT_IDS")
	}
}
