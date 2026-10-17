package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type shareHandlerSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *shareHandlerSettings) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *shareHandlerSettings) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func newShareSchedulingRouter(settings service.SettingRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewShareSchedulingHandler(service.NewShareSchedulingService(nil, nil, settings))
	router := gin.New()
	share := router.Group("/share-scheduling")
	share.GET("/settings", h.GetSettings)
	share.PUT("/settings", h.UpdateSettings)
	share.GET("/groups", h.ListGroups)
	share.PUT("/groups/:id/policy", h.UpdatePolicy)
	share.POST("/groups/:id/preview", h.Preview)
	share.GET("/groups/:id/report", h.Report)
	share.GET("/groups/:id/decisions", h.Decisions)
	share.POST("/groups/:id/rebalance", h.Rebalance)
	share.GET("/accounts/:id/rules", h.AccountRules)
	return router
}

func TestShareSchedulingSettingsMergeStrictly(t *testing.T) {
	settings := &shareHandlerSettings{values: map[string]string{}}
	router := newShareSchedulingRouter(settings)

	w := schedulingTestCall(router, http.MethodGet, "/share-scheduling/settings", "")
	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Data service.ShareSchedulingSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, service.DefaultShareSchedulingSettings(), got.Data)

	w = schedulingTestCall(router, http.MethodPut, "/share-scheduling/settings", `{"enabled":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.True(t, got.Data.Enabled)
	require.Equal(t, 4, got.Data.AttributionSampleRate, "omitted fields keep their current value")

	for _, body := range []string{`{"enabled":true,"mode":"share"}`, `{"enabled":true} {}`, `{"attribution_sample_rate":0}`} {
		w = schedulingTestCall(router, http.MethodPut, "/share-scheduling/settings", body)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
	}
}

func TestShareSchedulingHandlerRejectsMalformedRequests(t *testing.T) {
	router := newShareSchedulingRouter(&shareHandlerSettings{values: map[string]string{}})
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPut, "/share-scheduling/groups/abc/policy", `{"expected_version":0,"policy":{}}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"policy":{"scheduler":"share"}}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"expected_version":0,"policy":{},"force":true}`},
		{http.MethodPost, "/share-scheduling/groups/7/preview", `{}`},
		{http.MethodGet, "/share-scheduling/groups/7/report?window=48h", ""},
		{http.MethodGet, "/share-scheduling/groups/7/report?window=30s", ""},
		{http.MethodGet, "/share-scheduling/groups/7/decisions?limit=0", ""},
		{http.MethodPost, "/share-scheduling/groups/7/rebalance", `{"mode":"cold","dry_run":true}`},
		{http.MethodGet, "/share-scheduling/accounts/0/rules", ""},
	} {
		w := schedulingTestCall(router, call.method, call.path, call.body)
		require.Equal(t, http.StatusBadRequest, w.Code, "%s %s", call.method, call.path)
	}
	w := schedulingTestCall(router, http.MethodGet, "/share-scheduling/groups", "")
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "without storage the API says so instead of returning empty data")
	w = schedulingTestCall(router, http.MethodGet, "/share-scheduling/accounts/7/rules", "")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
