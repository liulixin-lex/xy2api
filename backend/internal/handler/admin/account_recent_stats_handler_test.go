//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type recentStatsHandlerRepo struct {
	ids   []int64
	calls int
	rows  []service.AccountRecentStatsRow
}

func (r *recentStatsHandlerRepo) Batch(_ context.Context, ids []int64, _ time.Time, _ bool) ([]service.AccountRecentStatsRow, error) {
	r.ids, r.calls = ids, r.calls+1
	return r.rows, nil
}
func recentStatsHandlerFixture() (*gin.Engine, *recentStatsHandlerRepo, *service.OpsService) {
	gin.SetMode(gin.TestMode)
	repo, ops := &recentStatsHandlerRepo{}, &service.OpsService{}
	h := NewAccountRecentStatsHandler(service.NewAccountRecentStatsService(repo, ops))
	router := gin.New()
	router.POST("/api/v1/admin/accounts/recent-stats/batch", h.Batch)
	return router, repo, ops
}
func recentStatsRequest(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/recent-stats/batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
func recentStatsData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	return envelope.Data
}
func TestRecentStatsHandlerScenario23NullsAndEmpty(t *testing.T) {
	r, repo, _ := recentStatsHandlerFixture()
	repo.rows = []service.AccountRecentStatsRow{{AccountID: 2, ErrorN: 2, ErrorBad: 2}}
	data := recentStatsData(t, recentStatsRequest(r, `{"account_ids":[2,1,2]}`))
	items := data["items"].([]any)
	require.Len(t, items, 2)
	encoded, err := json.Marshal(items)
	require.NoError(t, err)
	require.JSONEq(t, `[{"account_id":1,"ttft":{"p50_ms":null,"n":0,"tier":null},"error":{"rate":null,"bad":0,"n":0,"tier":null},"cache":{"ratio":null,"n":0,"tier":null}},{"account_id":2,"ttft":{"p50_ms":null,"n":0,"tier":null},"error":{"rate":1,"bad":2,"n":2,"tier":null},"cache":{"ratio":null,"n":0,"tier":null}}]`, string(encoded))
	require.Equal(t, []int64{1, 2}, repo.ids)
	asOf, err := time.Parse(time.RFC3339, data["as_of"].(string))
	require.NoError(t, err)
	require.Zero(t, asOf.Unix()%15)
	empty := recentStatsData(t, recentStatsRequest(r, `{"account_ids":[]}`))
	require.Equal(t, []any{}, empty["items"])
	require.Equal(t, 1, repo.calls)
}
func TestRecentStatsHandlerScenario24Monitoring(t *testing.T) {
	r, repo, ops := recentStatsHandlerFixture()
	repo.rows = []service.AccountRecentStatsRow{{AccountID: 1, ErrorN: 100, ErrorBad: 1}}
	read := func() any {
		return recentStatsData(t, recentStatsRequest(r, `{"account_ids":[1]}`))["items"].([]any)[0].(map[string]any)["error"]
	}
	require.NotNil(t, read())
	ops.SetMonitoringEnabled(false)
	require.Nil(t, read())
	repo.rows[0].ErrorBad = 2
	ops.SetMonitoringEnabled(true)
	require.Equal(t, .02, read().(map[string]any)["rate"])
}
func TestRecentStatsHandlerScenario25HundredAccounts(t *testing.T) {
	r, repo, _ := recentStatsHandlerFixture()
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(100 - i)
	}
	body, err := json.Marshal(map[string]any{"account_ids": ids})
	require.NoError(t, err)
	items := recentStatsData(t, recentStatsRequest(r, string(body)))["items"].([]any)
	require.Len(t, items, 100)
	for i, item := range items {
		require.EqualValues(t, i+1, item.(map[string]any)["account_id"])
	}
	require.Equal(t, 1, repo.calls)
}
func TestRecentStatsHandlerValidatesIDs(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"account_ids":null}`, `{"account_ids":[0]}`, `{"account_ids":[-1]}`, `{"account_ids":[1.5]}`, `{"account_ids":["1"]}`, `{"account_ids":[null]}`, `{"account_ids":[9223372036854775808]}`} {
		t.Run(body, func(t *testing.T) {
			r, repo, _ := recentStatsHandlerFixture()
			require.Equal(t, http.StatusBadRequest, recentStatsRequest(r, body).Code)
			require.Zero(t, repo.calls)
		})
	}
	for _, n := range []int{1000, 1001} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r, repo, _ := recentStatsHandlerFixture()
			ids := make([]int64, n)
			for i := range ids {
				ids[i] = int64(i + 1)
			}
			body, err := json.Marshal(map[string]any{"account_ids": ids})
			require.NoError(t, err)
			w := recentStatsRequest(r, string(body))
			if n == 1001 {
				require.Equal(t, 400, w.Code)
				require.Zero(t, repo.calls)
			} else {
				require.Len(t, recentStatsData(t, w)["items"], 1000)
			}
		})
	}
	r, repo, _ := recentStatsHandlerFixture()
	body := `{"account_ids":[` + strings.Repeat("1,", 1000) + `1]}`
	require.Len(t, recentStatsData(t, recentStatsRequest(r, body))["items"], 1)
	require.Equal(t, []int64{1}, repo.ids)
}
