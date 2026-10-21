//go:build unit

package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSpecialExhaustedPathsShareTurnCap(t *testing.T) {
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions, "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(endpoint+boolName(stream), func(t *testing.T) {
				withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.TurnCap.Failures = 2 })
				resetTurnCap(t)
				for attempt := 1; attempt <= 2; attempt++ {
					c, rec := turnCapContext(t, endpoint)
					if stream {
						c.Writer.Header().Set("Content-Type", "text/event-stream")
						n, err := c.Writer.WriteString(":\n\n")
						require.NoError(t, err)
						recordGatewayStreamHeartbeat(c, n)
						c.Writer.Flush()
					}
					failure := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseHeaders: http.Header{"Retry-After": {"60"}}}
					write := func() {
						switch endpoint {
						case EndpointResponses:
							(&GatewayHandler{}).handleResponsesFailoverExhausted(c, failure, stream)
						case EndpointChatCompletions:
							(&GatewayHandler{}).handleCCFailoverExhausted(c, failure, stream)
						default:
							(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, failure, stream)
						}
					}
					write()
					require.NotEmpty(t, strings.TrimPrefix(rec.Body.String(), ":\n\n"), "committed errors still need a terminal")
					if attempt == 1 {
						require.Equal(t, "30", c.Writer.Header().Get("Retry-After"))
					} else {
						require.Empty(t, c.Writer.Header().Get("Retry-After"))
						require.Equal(t, "false", c.Writer.Header().Get("x-should-retry"))
						require.Contains(t, rec.Body.String(), codeServerIsOverloaded)
						require.NotContains(t, rec.Body.String(), "try again in")
						if !stream {
							require.Equal(t, http.StatusServiceUnavailable, rec.Code)
						}
					}
					body := rec.Body.String()
					write()
					require.Equal(t, body, rec.Body.String(), "one terminal per request")
				}
			})
		}
	}
}

func TestExhaustedRetryHintCountsRequestOnlyOnce(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.TurnCap.Failures = 2 })
	resetTurnCap(t)
	c, _ := turnCapContext(t, EndpointResponses)
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusBadGateway, applyExhaustedRetryHint(c, nil, http.StatusBadGateway))
		require.False(t, c.GetBool(ginKeyResilienceTurnCapped), "helpers for the same request must not multiply failures")
	}
	c, _ = turnCapContext(t, EndpointResponses)
	require.Equal(t, http.StatusServiceUnavailable, applyExhaustedRetryHint(c, nil, http.StatusBadGateway))
}

func TestResponsesTerminalWriterFinalizesOnce(t *testing.T) {
	withHandlerResilience(t, nil)
	c, rec := newGinContextForEndpoint(t, EndpointResponses)
	require.True(t, writeResponsesFailedSSE(c, "api_error", "", "first"))
	before := rec.Body.String()
	require.True(t, writeResponsesFailedSSE(c, "api_error", "", "second"))
	require.Equal(t, before, rec.Body.String())
}
