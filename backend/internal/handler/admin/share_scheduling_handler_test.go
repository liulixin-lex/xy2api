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
	share.GET("/groups/:id/decisions", RetiredShareSchedulingEndpoint)
	share.POST("/groups/:id/rebalance", RetiredShareSchedulingEndpoint)
	share.GET("/accounts/:id/rules", h.AccountRules)
	return router
}

// TestShareSchedulingSettingsAcceptRetiredSwitches: the settings carry the
// master switch only; an older admin page may still send the retired
// switches, which are ignored (design §7.1).
func TestShareSchedulingSettingsAcceptRetiredSwitches(t *testing.T) {
	settings := &shareHandlerSettings{values: map[string]string{}}
	router := newShareSchedulingRouter(settings)

	w := schedulingTestCall(router, http.MethodGet, "/share-scheduling/settings", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":false}`, string(shareResponseData(t, w.Body.Bytes())))

	w = schedulingTestCall(router, http.MethodPut, "/share-scheduling/settings",
		`{"enabled":true,"cache_attribution":false,"attribution_sample_rate":0,"decision_samples":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"enabled":true}`, string(shareResponseData(t, w.Body.Bytes())))
	require.JSONEq(t, `{"enabled":true}`, settings.values["share_scheduling_settings"], "retired switches are not stored")

	w = schedulingTestCall(router, http.MethodPut, "/share-scheduling/settings", `{}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":true}`, string(shareResponseData(t, w.Body.Bytes())), "an omitted switch keeps its value")

	for _, body := range []string{`{"enabled":true,"mode":"share"}`, `{"enabled":true} {}`, `{"enabled":"yes"}`} {
		w = schedulingTestCall(router, http.MethodPut, "/share-scheduling/settings", body)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
	}
}

func shareResponseData(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope.Data
}

func TestShareSchedulingHandlerRejectsMalformedRequests(t *testing.T) {
	router := newShareSchedulingRouter(&shareHandlerSettings{values: map[string]string{}})
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPut, "/share-scheduling/groups/abc/policy", `{"expected_version":0,"scheduler":"share","session_keep":true}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"scheduler":"share","session_keep":true}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"expected_version":0,"scheduler":"share"}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"expected_version":0,"scheduler":"shadow","session_keep":true}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"expected_version":0,"scheduler":"share","session_keep":true,"force":true}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"expected_version":0,"policy":{"scheduler":"share","bogus":1}}`},
		{http.MethodPut, "/share-scheduling/groups/7/policy", `{"expected_version":0,"scheduler":"share","session_keep":true,"policy":{"scheduler":"share"}}`},
		{http.MethodPost, "/share-scheduling/groups/7/preview", `{}`},
		{http.MethodGet, "/share-scheduling/groups/7/report?window=48h", ""},
		{http.MethodGet, "/share-scheduling/groups/7/report?window=30s", ""},
		{http.MethodGet, "/share-scheduling/accounts/0/rules", ""},
	} {
		w := schedulingTestCall(router, call.method, call.path, call.body)
		require.Equal(t, http.StatusBadRequest, w.Code, "%s %s %s", call.method, call.path, call.body)
	}
	// Both request shapes pass validation and reach the service.
	for _, body := range []string{
		`{"expected_version":0,"scheduler":"share","session_keep":true}`,
		`{"expected_version":3,"policy":{"schema":1,"scheduler":"shadow","accounts":[{"account_id":1,"tier":1,"weight":3}]}}`,
	} {
		w := schedulingTestCall(router, http.MethodPut, "/share-scheduling/groups/7/policy", body)
		require.Equal(t, http.StatusServiceUnavailable, w.Code, body)
	}
	w := schedulingTestCall(router, http.MethodPost, "/share-scheduling/groups/7/preview", `{"scheduler":"share","session_keep":false,"expected_version":2}`)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	w = schedulingTestCall(router, http.MethodGet, "/share-scheduling/groups", "")
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "without storage the API says so instead of returning empty data")
	w = schedulingTestCall(router, http.MethodGet, "/share-scheduling/accounts/7/rules", "")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRetiredShareSchedulingRoutesAnswerGone(t *testing.T) {
	router := newShareSchedulingRouter(&shareHandlerSettings{values: map[string]string{}})
	for _, call := range []struct{ method, path, body string }{
		{http.MethodGet, "/share-scheduling/groups/7/decisions?limit=5", ""},
		{http.MethodPost, "/share-scheduling/groups/7/rebalance", `{"mode":"all"}`},
	} {
		w := schedulingTestCall(router, call.method, call.path, call.body)
		require.Equal(t, http.StatusGone, w.Code, call.path)
		var body struct {
			Code   int    `json:"code"`
			Reason string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, http.StatusGone, body.Code)
		require.Equal(t, "SHARE_SCHEDULING_ENDPOINT_RETIRED", body.Reason)
	}
}
