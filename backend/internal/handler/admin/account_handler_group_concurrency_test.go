//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// accountListGroupSlotCacheStub serves totals and per-group slots; the
// embedded ConcurrencyCache is nil, so any other call would panic.
type accountListGroupSlotCacheStub struct {
	service.ConcurrencyCache
	totals     map[int64]int
	groups     map[int64]map[int64]int
	groupCalls int
}

func (s *accountListGroupSlotCacheStub) GetAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.totals[id]
	}
	return out, nil
}

func (s *accountListGroupSlotCacheStub) GetAccountGroupConcurrencyBatch(_ context.Context, _ []int64) (map[int64]map[int64]int, error) {
	s.groupCalls++
	return s.groups, nil
}

func TestAccountHandlerListReturnsGroupConcurrencyOnlyForAGroupFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{
		{ID: 501, Name: "shared", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10, 20}, CreatedAt: now, UpdatedAt: now},
		{ID: 502, Name: "other-group-only", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10}, CreatedAt: now, UpdatedAt: now},
		{ID: 503, Name: "between-reads", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10}, CreatedAt: now, UpdatedAt: now},
	}
	cache := &accountListGroupSlotCacheStub{
		totals: map[int64]int{501: 5, 502: 1, 503: 0},
		// 503: a slot taken after the total was read shows up in the group read only
		groups: map[int64]map[int64]int{501: {10: 3, 20: 2}, 502: {20: 1}, 503: {10: 1}},
	}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, service.NewConcurrencyService(cache), nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/accounts", handler.List)

	list := func(query string) []map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20"+query, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var payload struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		require.Len(t, payload.Data.Items, 3)
		return payload.Data.Items
	}

	for _, query := range []string{"&group=10&lite=1", "&group=10"} {
		items := list(query)
		require.Equal(t, float64(5), items[0]["current_concurrency"], query)
		require.Equal(t, float64(3), items[0]["current_group_concurrency"], query)
		require.Equal(t, float64(0), items[1]["current_group_concurrency"], "%s: slots of other groups do not count", query)
		require.Equal(t, float64(0), items[2]["current_group_concurrency"], "%s: the third number never exceeds the first", query)
	}
	require.Equal(t, float64(2), list("&group=20&lite=1")[0]["current_group_concurrency"])
	callsWithFilter := cache.groupCalls
	require.Equal(t, 3, callsWithFilter)

	for _, query := range []string{"&lite=1", "", "&group=ungrouped&lite=1"} {
		for _, item := range list(query) {
			require.NotContains(t, item, "current_group_concurrency", query)
			require.Contains(t, item, "current_concurrency", query)
		}
	}
	require.Equal(t, callsWithFilter, cache.groupCalls, "no group read without a specific group filter")
}
