//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCompatibleExhaustionAfterOutputDoesNotCountTurn(t *testing.T) {
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
		t.Run(endpoint, func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.TurnCap.Failures = 1 })
			resetTurnCap(t)
			c, w := turnCapContext(t, endpoint)
			_, err := c.Writer.WriteString("data: {\"text\":\"actual output\"}\n\n")
			require.NoError(t, err)
			c.Writer.Flush()
			before := w.Body.String()
			failure := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseHeaders: http.Header{"Retry-After": {"60"}}}
			if endpoint == EndpointResponses {
				(&GatewayHandler{}).handleResponsesFailoverExhausted(c, failure, true)
			} else {
				(&GatewayHandler{}).handleCCFailoverExhausted(c, failure, true)
			}
			require.Equal(t, before, w.Body.String())
			require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
			require.False(t, resilience.StateFrom(c.Request.Context()).TurnFailed())
			require.Empty(t, c.Writer.Header().Get("Retry-After"))
			require.Empty(t, c.Writer.Header().Get("x-should-retry"))
		})
	}
}
