package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func schedulingTestCall(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestRetiredSchedulingRoutesAnswerGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/accounts/:id/scheduling-control", RetiredScheduling)
	router.PUT("/scheduling/mode", RetiredScheduling)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/accounts/1/scheduling-control", `{"action":"force_stop"}`},
		{http.MethodPut, "/scheduling/mode", `{"mode":"controlled"}`},
	} {
		w := schedulingTestCall(router, call.method, call.path, call.body)
		require.Equal(t, http.StatusGone, w.Code, call.path)
		var body struct {
			Reason   string            `json:"reason"`
			Metadata map[string]string `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "SCHEDULING_RETIRED", body.Reason)
		require.Equal(t, "/api/v1/admin/share-scheduling", body.Metadata["replacement"])
	}
}
