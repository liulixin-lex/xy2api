//go:build unit

package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const webSearchRejectedClientMessage = "Upstream rejected the web_search history in this request: response protection is unavailable"

func webSearchHistoryRejectedError() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:        http.StatusBadGateway,
		Stage:             service.GatewayFailureStageInference,
		Scope:             service.GatewayFailureScopeRequest,
		Reason:            service.OpenAIWebSearchHistoryRejectedReason,
		NextAccountAction: service.NextAccountStop,
		ClientStatusCode:  http.StatusBadGateway,
		ClientMessage:     webSearchRejectedClientMessage,
	}
}

func TestWebSearchHistoryRejectedKeepsReasonAndEndsTheTurn(t *testing.T) {
	// Spec (R3): when the declared replay is rejected too, the client gets
	// the upstream's reason, not a generic or "contact administrator" text,
	// and the failure counts toward the failed-turn cap so Codex stops.
	withHandlerResilience(t, nil)
	resetTurnCap(t)
	for i := 1; i <= 4; i++ {
		c, w := turnCapContext(t, "")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, webSearchHistoryRejectedError(), false)
		require.Equal(t, http.StatusBadGateway, w.Code, "failure %d", i)
		require.Equal(t, "upstream_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
		require.Contains(t, gjson.GetBytes(w.Body.Bytes(), "error.message").String(), webSearchRejectedClientMessage)
		require.NotContains(t, w.Body.String(), "administrator")
		require.NotEmpty(t, w.Header().Get("Retry-After"))
	}
	c, w := turnCapContext(t, EndpointResponses)
	_, _ = c.Writer.WriteString(":\n\n") // pre-output heartbeat committed the stream
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, webSearchHistoryRejectedError(), true)
	_, errObj := parseResponsesFailedSSE(t, strings.TrimPrefix(w.Body.String(), ":\n\n"))
	require.Equal(t, "server_is_overloaded", errObj["code"])
	require.Equal(t, webSearchRejectedClientMessage, errObj["message"])
	require.True(t, resilience.StateFrom(c.Request.Context()).TurnFailed())

	// Other failures keep their handling.
	_, _, _, ok := resilienceWebSearchHistoryRejected(c, nil, &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway})
	require.False(t, ok)
}
