//go:build unit

package admin

import (
	"bytes"
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

type recentHealthRepoStub struct {
	service.UsageLogRepository
	calls int
	ids   []int64
	stats map[int64]service.AccountRecentHealth
}

func (s *recentHealthRepoStub) GetAccountRecentHealthBatch(_ context.Context, accountIDs []int64, _, _, _ time.Time) (map[int64]service.AccountRecentHealth, error) {
	s.calls++
	s.ids = append([]int64(nil), accountIDs...)
	return s.stats, nil
}

// The reader is matched by type assertion, so a stale stub signature would only fail at runtime.
var _ service.AccountRecentHealthBatchReader = (*recentHealthRepoStub)(nil)

func postRecentHealth(t *testing.T, repo *recentHealthRepoStub, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := &AccountHandler{
		accountUsageService: service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil),
	}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/recent-health/batch", handler.GetBatchRecentHealth)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/recent-health/batch", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

type recentHealthResponse struct {
	Data struct {
		Stats                   map[string]service.AccountRecentHealth `json:"stats"`
		FirstTokenWindowSeconds int                                    `json:"first_token_window_seconds"`
		ErrorsWindowSeconds     int                                    `json:"errors_window_seconds"`
		CacheWindowSeconds      int                                    `json:"cache_window_seconds"`
	} `json:"data"`
}

func TestGetBatchRecentHealthReturnsStatsKeyedByAccountAndCachesThem(t *testing.T) {
	accountRecentHealthBatchCache = newSnapshotCache(10 * time.Second)
	avg := int64(850)
	repo := &recentHealthRepoStub{stats: map[int64]service.AccountRecentHealth{
		11: {FirstTokenAvgMs: &avg, FirstTokenSamples: 3, RecentErrors: 5, InputTokens: 3, CacheCreationTokens: 120, CacheReadTokens: 877, CacheSamples: 4},
	}}

	first := postRecentHealth(t, repo, `{"account_ids":[11,12,11,0]}`)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"))
	var body recentHealthResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &body))
	require.Equal(t, []int64{11, 12}, repo.ids)
	require.Equal(t, 180, body.Data.FirstTokenWindowSeconds)
	require.Equal(t, 600, body.Data.ErrorsWindowSeconds)
	require.Equal(t, 1800, body.Data.CacheWindowSeconds)
	require.Equal(t, int64(850), *body.Data.Stats["11"].FirstTokenAvgMs)
	require.Equal(t, 5, body.Data.Stats["11"].RecentErrors)
	require.Equal(t, int64(3), body.Data.Stats["11"].InputTokens)
	require.Equal(t, int64(120), body.Data.Stats["11"].CacheCreationTokens)
	require.Equal(t, int64(877), body.Data.Stats["11"].CacheReadTokens)
	require.Equal(t, 4, body.Data.Stats["11"].CacheSamples)
	require.Contains(t, first.Body.String(), `"input_tokens":3,"cache_creation_tokens":120,"cache_read_tokens":877,"cache_samples":4`)
	require.Nil(t, body.Data.Stats["12"].FirstTokenAvgMs)
	require.Zero(t, body.Data.Stats["12"].RecentErrors)

	second := postRecentHealth(t, repo, `{"account_ids":[11,12]}`)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "hit", second.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.calls)
}

func TestGetBatchRecentHealthRejectsInvalidRequests(t *testing.T) {
	accountRecentHealthBatchCache = newSnapshotCache(10 * time.Second)
	tooMany := make([]int64, accountRecentHealthMaxIDs+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	tooManyBody, err := json.Marshal(map[string]any{"account_ids": tooMany})
	require.NoError(t, err)

	for name, body := range map[string]string{
		"invalid json":   `{"account_ids":`,
		"missing ids":    `{}`,
		"more than 1000": string(tooManyBody),
	} {
		t.Run(name, func(t *testing.T) {
			repo := &recentHealthRepoStub{}
			recorder := postRecentHealth(t, repo, body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Zero(t, repo.calls)
		})
	}
}

func TestGetBatchRecentHealthWithOnlyInvalidIDsReturnsEmptyStats(t *testing.T) {
	accountRecentHealthBatchCache = newSnapshotCache(10 * time.Second)
	repo := &recentHealthRepoStub{}
	recorder := postRecentHealth(t, repo, `{"account_ids":[0,-1]}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	var body recentHealthResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Empty(t, body.Data.Stats)
	require.Zero(t, repo.calls)
}
