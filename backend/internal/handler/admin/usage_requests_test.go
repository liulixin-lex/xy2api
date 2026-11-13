package admin

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminRequestListStatsUseIdenticalFilters(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)
	query := "?result=4&request_id=synthetic&start_date=2026-11-01&end_date=2026-11-02&timezone=America/Los_Angeles&model=fixture&account_id=7&request_type=ws_v2&nocache=true"
	for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path+query, nil))
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	require.Equal(t, repo.listFilters, repo.statsFilters)
	require.Equal(t, int16(4), *repo.listFilters.Result)
	require.Equal(t, "synthetic", repo.statsFilters.RequestID)
	require.EqualValues(t, 49, repo.statsFilters.EndTime.Sub(*repo.statsFilters.StartTime).Hours(), "local dates must preserve the DST boundary")
	for _, query := range []string{"?result=0", "?result=5", "?result=failed", "?start_date=2026-11-03&end_date=2026-11-01"} {
		for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", path+query, nil))
			require.Equal(t, 400, w.Code, query)
		}
	}
}

func TestAdminUnbilledResponsePreservesUnknownsAndRealIDs(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/admin/usage", nil)
	result := int16(4)
	r := service.AdminUsageRecord{Source: "unbilled", Result: &result, Log: service.UsageLog{ID: 12, Model: "fixture"}}
	encoded, err := json.Marshal(adminUsageRequestResponses(c, []service.AdminUsageRecord{r}))
	require.NoError(t, err)
	var data []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &data))
	require.JSONEq(t, `12`, string(data[0]["id"]))
	require.JSONEq(t, `"unbilled"`, string(data[0]["source"]))
	require.JSONEq(t, `4`, string(data[0]["result"]))
	require.JSONEq(t, `false`, string(data[0]["has_attempts"]))
	for _, field := range []string{"account_id", "account", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "input_cost", "output_cost", "total_cost", "actual_cost", "end_to_end_first_output_ms", "ttfo_over"} {
		require.JSONEq(t, `null`, string(data[0][field]), field)
	}
}
