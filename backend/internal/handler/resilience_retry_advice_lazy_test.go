//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// P2-10: the candidate advice only goes into a failed frame. A plain JSON
// no-account or exhausted exit must not query it; a committed stream still
// resolves it once, with the upstream hint as its fallback.
func TestRetryAdviceResolvesOnlyForFailedFrames(t *testing.T) {
	withHandlerResilience(t, nil)
	group := int64(20)
	apiKey := &service.APIKey{GroupID: &group}
	noAccount := noAccountErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: "No available accounts"}

	t.Run("no_account_json", func(t *testing.T) {
		c, rec := upstreamWaitContext(t, EndpointResponses, false)
		advisor := &upstreamWaitAdvisor{advice: resilience.RetryAdvice{Seconds: 1, Source: "healthy_candidate"}}
		applyNoAccountRetryHints(c, advisor, apiKey, "model", noAccount)
		c.JSON(noAccount.Status, gin.H{"error": gin.H{"type": noAccount.ErrType, "message": noAccount.Message}})
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.NotEmpty(t, c.Writer.Header().Get("Retry-After"), "the HTTP hint does not depend on the candidate advice")
		require.Zero(t, advisor.calls.Load(), "a JSON response never carries the candidate advice")
	})

	t.Run("exhausted_json", func(t *testing.T) {
		c, rec := upstreamWaitContext(t, EndpointResponses, false)
		copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
		advisor := &upstreamWaitAdvisor{advice: resilience.RetryAdvice{Source: "unknown"}}
		status := applyExhaustedRetryHint(c, advisor, http.StatusServiceUnavailable)
		(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "server_error", "Busy", false)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Equal(t, "8", c.Writer.Header().Get("Retry-After"))
		require.Zero(t, advisor.calls.Load(), "a JSON response never carries the candidate advice")
	})

	t.Run("exhausted_committed_stream", func(t *testing.T) {
		c, rec := upstreamWaitContext(t, EndpointResponses, true)
		copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
		advisor := &upstreamWaitAdvisor{advice: resilience.RetryAdvice{Source: "unknown"}}
		status := applyExhaustedRetryHint(c, advisor, http.StatusServiceUnavailable)
		require.Zero(t, advisor.calls.Load(), "the exit only prepares the advice")
		(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "server_error", "Busy", true)
		failed := upstreamWaitFailure(t, rec.Body.String())
		require.Equal(t, "8", failed.Get("headers.retry-after").String())
		require.Equal(t, int64(1), advisor.calls.Load())
	})
}
