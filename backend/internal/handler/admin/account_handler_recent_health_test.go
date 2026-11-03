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
	calls        int
	ids          []int64
	observations map[int64][]service.GroupQualityObservation
}

func (s *recentHealthRepoStub) GetAccountRecentHealthBatch(_ context.Context, accountIDs []int64, _ time.Time) (map[int64][]service.GroupQualityObservation, error) {
	s.calls++
	s.ids = append([]int64(nil), accountIDs...)
	return s.observations, nil
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
		Stats                map[string]service.AccountRecentHealth `json:"stats"`
		FailureMaxEvents     int                                    `json:"failure_max_events"`
		FailureMaxAgeSeconds int                                    `json:"failure_max_age_seconds"`
		SampleMax            int                                    `json:"sample_max"`
		SampleMaxAgeSeconds  int                                    `json:"sample_max_age_seconds"`
		MinDisplaySamples    int                                    `json:"min_display_samples"`
		MinColorSamples      int                                    `json:"min_color_samples"`
	} `json:"data"`
}

func TestGetBatchRecentHealthReturnsStatsKeyedByAccountAndCachesThem(t *testing.T) {
	accountRecentHealthBatchCache = newSnapshotCache(10 * time.Second)
	base := time.Now().UTC()
	repo := &recentHealthRepoStub{observations: map[int64][]service.GroupQualityObservation{
		11: {{
			AccountID: 11, RequestID: "r1", At: base.Add(-time.Minute), Success: true,
			StrictContinuation: true, Streaming: true, FirstOutputMs: 9000,
			PromptTokens: 4096, PreviousPromptTokens: 2048, CacheReadTokens: 1024,
		}},
	}}

	first := postRecentHealth(t, repo, `{"account_ids":[11,12,11,0]}`)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"))
	var body recentHealthResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &body))
	require.Equal(t, []int64{11, 12}, repo.ids)
	require.Equal(t, service.AccountRecentFailureMaxEvents, body.Data.FailureMaxEvents)
	require.Equal(t, int(service.AccountRecentFailureWindow/time.Second), body.Data.FailureMaxAgeSeconds)
	require.Equal(t, service.AccountRecentSampleMax, body.Data.SampleMax)
	require.Equal(t, int(service.AccountRecentSampleWindow/time.Second), body.Data.SampleMaxAgeSeconds)
	require.Equal(t, service.AccountRecentMinDisplaySamples, body.Data.MinDisplaySamples)
	require.Equal(t, service.AccountRecentMinColorSamples, body.Data.MinColorSamples)
	require.Equal(t, 1, body.Data.Stats["11"].Failure.Calls)
	require.NotNil(t, body.Data.Stats["11"].FirstOutput)
	require.NotNil(t, body.Data.Stats["11"].Cache)
	require.NotContains(t, body.Data.Stats, "12", "accounts with no metrics may be omitted")
	require.NotContains(t, first.Body.String(), "first_token_avg_ms")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &payload))
	data := payload["data"].(map[string]any)
	stats := data["stats"].(map[string]any)
	accountStats := stats["11"].(map[string]any)
	failure := accountStats["failure"].(map[string]any)
	firstOutput := accountStats["first_output"].(map[string]any)
	cache := accountStats["cache"].(map[string]any)
	require.Contains(t, failure, "wilson_low")
	require.Contains(t, firstOutput, "p90_ms")
	require.Contains(t, cache, "kept")

	second := postRecentHealth(t, repo, `{"account_ids":[11,12]}`)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "hit", second.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, 1, repo.calls)
	require.NotEmpty(t, first.Header().Get("ETag"))

	notModified := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/recent-health/batch", bytes.NewBufferString(`{"account_ids":[11,12]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-None-Match", second.Header().Get("ETag"))
	gin.SetMode(gin.TestMode)
	handler := &AccountHandler{accountUsageService: service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)}
	router := gin.New()
	router.POST("/api/v1/admin/accounts/recent-health/batch", handler.GetBatchRecentHealth)
	router.ServeHTTP(notModified, request)
	require.Equal(t, http.StatusNotModified, notModified.Code)
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
