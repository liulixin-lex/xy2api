package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDetailAndSavedResponseUseIQTargetContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = &service.Account{ID: 42, Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	adminSvc.iqTargetSummaries = map[int64][]domain.IQTargetSummary{
		42: {{Model: "gpt-6-astra", Enabled: true, Verdict: "unknown", State: "idle"}},
	}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/accounts/:id", handler.GetByID)
	router.PUT("/accounts/:id", handler.Update)

	for _, tc := range []struct {
		name   string
		method string
		body   string
	}{
		{name: "detail", method: http.MethodGet},
		{name: "saved response", method: http.MethodPut, body: `{"iq_check":{"enabled":true,"model":"gpt-6-astra","interval_minutes":15}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, "/accounts/42", bytes.NewBufferString(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

			var response struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.NotContains(t, response.Data, "iq_check")
			var targets []domain.IQTargetSummary
			require.NoError(t, json.Unmarshal(response.Data["iq_targets"], &targets))
			require.Len(t, targets, 1)
			require.Equal(t, "gpt-6-astra", targets[0].Model)
		})
	}
	require.NotNil(t, adminSvc.lastUpdateAccountInput.IQTargets)
	require.Len(t, adminSvc.lastUpdateAccountInput.IQTargets.Targets, 1)
	require.Equal(t, "gpt-6-astra", adminSvc.lastUpdateAccountInput.IQTargets.Targets[0].Model)
}
