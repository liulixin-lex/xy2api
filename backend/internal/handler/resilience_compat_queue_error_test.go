//go:build unit

package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
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
						cache := &helperConcurrencyCacheStub{accountSeq: []bool{false, true}}
						h := &GatewayHandler{concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, time.Millisecond)}
						started := false
						release, err := h.concurrencyHelper.waitForSlotWithPingTimeout(c, "account", 1, 1, time.Second, true, &started, true)
						require.NoError(t, err)
						defer release()
						require.True(t, started)
						require.Empty(t, parseSSEEvents(rec.Body.String()), "queue pings must not be protocol data events")
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
