//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These fields are fixed by the JSON writers at the sprint baseline 027a434ad.
// Protocol completion and retry hints may add fields without renaming them.
func TestCompatibleBillingErrorsPreserveBaselineFields(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, kind := range []string{errTypeInsufficientBalance, errTypeBalanceReserved} {
		for _, tc := range []struct {
			endpoint, field string
			write           func(*gin.Context, int, string, string)
		}{
			{EndpointResponses, "error.code", (&GatewayHandler{}).responsesErrorResponse},
			{EndpointChatCompletions, "error.type", (&GatewayHandler{}).chatCompletionsErrorResponse},
			{"/v1/messages", "error.type", (&OpenAIGatewayHandler{}).anthropicErrorResponse},
		} {
			t.Run(tc.endpoint+kind, func(t *testing.T) {
				c, w := newGinContextForEndpoint(t, tc.endpoint)
				status := http.StatusForbidden
				if kind == errTypeBalanceReserved {
					status = http.StatusTooManyRequests
				}
				tc.write(c, status, kind, "billing failure")
				require.Equal(t, status, w.Code)
				require.Equal(t, kind, gjson.GetBytes(w.Body.Bytes(), tc.field).String())
				require.Equal(t, "billing failure", gjson.GetBytes(w.Body.Bytes(), "error.message").String())
			})
		}
	}
	// The Anthropic stream already had an error.type at the baseline too.
	c, w := newGinContextForEndpoint(t, "/v1/messages")
	(&OpenAIGatewayHandler{}).anthropicStreamingAwareError(c, http.StatusTooManyRequests, errTypeBalanceReserved, "billing failure", true)
	events := parseSSEEvents(w.Body.String())
	require.Len(t, events, 1)
	require.Equal(t, errTypeBalanceReserved, gjson.Get(events[0].data, "error.type").String())
}
