//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompatibleQueueErrorsEndInInboundProtocol(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, tracked := range []bool{true, false} {
			for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
				for _, failure := range []struct {
					name   string
					status int
					kind   string
				}{
					{"balance", http.StatusForbidden, errTypeInsufficientBalance},
					{"reservation", http.StatusTooManyRequests, errTypeBalanceReserved},
					{"billing_dependency", http.StatusServiceUnavailable, "billing_service_error"},
					{"model", http.StatusNotFound, "model_not_found"},
				} {
					t.Run(failure.name+endpoint+boolName(enabled)+boolName(tracked), func(t *testing.T) {
						withHandlerResilience(t, func(s *resilience.Settings) { s.Enabled = enabled })
						c, rec := newGinContextForEndpoint(t, endpoint)
						if tracked {
							c, rec = trackedResponsesContext(t)
							c.Request.URL.Path = endpoint
						}
						c.Writer.Header().Set("Content-Type", "text/event-stream")
						n, err := c.Writer.WriteString(":\n\n")
						require.NoError(t, err)
						recordGatewayStreamHeartbeat(c, n)
						c.Writer.Flush()
						h := &GatewayHandler{}
						if endpoint == EndpointResponses {
							h.responsesErrorResponse(c, failure.status, failure.kind, "queue failure")
							failed := requireCreatedThenFailed(t, rec.Body.String())
							require.Equal(t, "queue failure", failed.Get("error.message").String())
						} else {
							h.chatCompletionsErrorResponse(c, failure.status, failure.kind, "queue failure")
							events := parseSSEEvents(rec.Body.String())
							require.Len(t, events, 2, rec.Body.String())
							require.Equal(t, "queue failure", gjson.Get(events[0].data, "error.message").String())
							require.Equal(t, "[DONE]", events[1].data)
						}
						require.Equal(t, http.StatusOK, rec.Code)
					})
				}
			}
		}
	}
}

func boolName(v bool) string {
	if v {
		return "/on"
	}
	return "/off"
}
