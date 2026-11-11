//go:build unit

package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSchedulingRetiredShareRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerRetiredSchedulingRoutes(router.Group("/api/v1/admin"))
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/share-scheduling/groups/7/decisions?limit=5"},
		{http.MethodPost, "/api/v1/admin/share-scheduling/groups/7/rebalance"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(call.method, call.path, strings.NewReader(`{"mode":"all"}`)))
		require.Equal(t, http.StatusGone, w.Code)
		var body struct {
			Reason string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "SHARE_SCHEDULING_ENDPOINT_RETIRED", body.Reason)
	}
}
