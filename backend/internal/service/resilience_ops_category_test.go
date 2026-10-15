//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpsUpstreamEventsCarryResilienceCategory(t *testing.T) {
	// tasks 1.2: the ops record keeps each attempt's failure category.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{UpstreamStatusCode: http.StatusPaymentRequired,
		UpstreamResponseBody: `{"error":{"message":"Insufficient balance. Please recharge."}}`, Kind: "failover"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{UpstreamStatusCode: http.StatusBadGateway,
		UpstreamResponseBody: "<html>502 Bad gateway</html>", Kind: "failover"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{UpstreamStatusCode: http.StatusOK,
		UpstreamResponseBody: `{"type":"response.failed","response":{"error":{"code":"insufficient_quota"}}}`, Kind: "stream_failed"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "request_error", Message: "dial tcp: timeout"})
	v, _ := c.Get(OpsUpstreamErrorsKey)
	events := v.([]*OpsUpstreamErrorEvent)
	require.Equal(t, "BALANCE", events[0].ResilienceCategory)
	require.Equal(t, "TRANSIENT", events[1].ResilienceCategory)
	require.Equal(t, "BALANCE", events[2].ResilienceCategory)
	require.Empty(t, events[3].ResilienceCategory)

	off := resilience.Defaults()
	off.Enabled = false
	resilience.Publish(off)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{UpstreamStatusCode: http.StatusBadGateway, Kind: "failover"})
	v, _ = c.Get(OpsUpstreamErrorsKey)
	require.Empty(t, v.([]*OpsUpstreamErrorEvent)[4].ResilienceCategory, "switch off: 0.3.0 record")
}

func TestUnclassifiedSamplingIsBounded(t *testing.T) {
	// tasks 1.3: about ten samples a minute, whatever the error rate.
	unclassifiedSampler.mu.Lock()
	unclassifiedSampler.tokens, unclassifiedSampler.last = unclassifiedSampleBurst, time.Time{}
	unclassifiedSampler.mu.Unlock()
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	allowed := 0
	for i := 0; i < 100; i++ {
		if sampleUnclassified(now) {
			allowed++
		}
	}
	require.Equal(t, unclassifiedSampleBurst, allowed)
	require.False(t, sampleUnclassified(now.Add(time.Second)))
	require.True(t, sampleUnclassified(now.Add(7*time.Second)), "refills at ten a minute")
}
