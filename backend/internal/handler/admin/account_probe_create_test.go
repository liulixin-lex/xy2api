package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBatchCreateForwardsExplicitProbeDisable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	h := &AccountHandler{adminService: svc}
	r := gin.New()
	r.POST("/accounts/batch", h.BatchCreate)
	req := httptest.NewRequest(http.MethodPost, "/accounts/batch", strings.NewReader(`{"accounts":[{"name":"off","platform":"openai","type":"apikey","credentials":{"api_key":"test"},"upstream_billing_probe_enabled":false}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, svc.createdAccounts, 1)
	require.NotNil(t, svc.createdAccounts[0].ProbeEnabled)
	require.False(t, *svc.createdAccounts[0].ProbeEnabled)
}
