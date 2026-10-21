//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestNonRetryableFailuresRemoveCandidateAdvice(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		status int
		typ    string
	}{
		{404, "model_not_found"}, {404, "not_found_error"}, {503, "billing_service_error"},
		{400, "api_error"}, {403, "permission_error"}, {422, "invalid_request_error"},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.typ), func(t *testing.T) {
			for _, failedFrame := range []bool{false, true} {
				c, w := turnCapContext(t, EndpointResponses)
				c.Header("Retry-After", "7")
				c.Set(ginKeyResilienceRetryAfter, 7)
				resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
				if failedFrame {
					(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, tc.status, tc.typ, "Unavailable. Please try again in 7s.", true)
					_, body := parseResponsesFailedSSE(t, w.Body.String())
					require.NotContains(t, body, "headers")
					require.NotContains(t, body["message"], "Please try again")
				} else {
					applyNoAccountRetryHints(c, nil, nil, "model", noAccountErrorClassification{Status: tc.status, ErrType: tc.typ, ModelNotFound: tc.typ == "model_not_found"})
				}
				require.Empty(t, c.Writer.Header().Get("Retry-After"))
				require.Zero(t, c.GetInt(ginKeyResilienceRetryAfter))
				require.True(t, resilience.RetryAdviceFrom(c.Request.Context()).Suppress)
				require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
			}
		})
	}
}

func TestTransientFailuresRetainCandidateAdvice(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
		c, w := newResilienceTestContext(t)
		c.Request.URL.Path = EndpointResponses
		resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
		(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "api_error", "Unavailable", true)
		_, body := parseResponsesFailedSSE(t, w.Body.String())
		require.Equal(t, map[string]any{"retry-after": "7"}, body["headers"])
	}
}
