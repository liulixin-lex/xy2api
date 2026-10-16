//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Behind a CDN edge a 502/504 reaches the client as 503 (Cloudflare would
// replace the body otherwise). The ops record keeps the gateway's own status,
// or 502s and 504s would count as no-account 503s (design §14 metrics).
func TestOpsRecordKeepsTheStatusBeforeTheEdgeRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		edge   string
		status int
		client int
		ops    int
	}{
		{"cloudflare", http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusBadGateway},
		{"cloudflare", http.StatusGatewayTimeout, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		{"cloudflare", http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable},
		{"", http.StatusBadGateway, http.StatusBadGateway, http.StatusBadGateway},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		st := resilience.NewRequestState()
		st.SetEdge(tc.edge)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
		service.InstallDeliveryTracking(c, time.Now())
		c.JSON(tc.status, gin.H{"error": gin.H{"message": "x"}})

		require.Equal(t, tc.client, rec.Code, "client status, edge %q", tc.edge)
		require.Equal(t, tc.ops, opsRecordedStatus(c), "ops status, edge %q status %d", tc.edge, tc.status)
	}
}
