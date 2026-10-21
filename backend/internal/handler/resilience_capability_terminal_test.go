//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type capabilityOnlySelectionFailure struct{}

func (capabilityOnlySelectionFailure) Error() string        { return "no available accounts" }
func (capabilityOnlySelectionFailure) CapabilityOnly() bool { return true }

func TestCapabilityOnlySelectionClearsRetryHints(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.TurnCap.Failures = 100 })
	resetTurnCap(t)
	c, _ := turnCapContext(t, EndpointResponses)
	c.Header("Retry-After", "7")
	c.Set(ginKeyResilienceRetryAfter, 7)
	resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
	fallback := noAccountErrorClassification{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable"}
	cls := classifySelectionFailureError(fmt.Errorf("selection: %w", capabilityOnlySelectionFailure{}), fallback)
	applyNoAccountRetryHints(c, nil, nil, "model", cls)
	require.Equal(t, service.CapabilityOnlyMessage, cls.Message)
	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.Empty(t, c.Writer.Header().Get("Retry-After"))
	require.Equal(t, "false", c.Writer.Header().Get("x-should-retry"))
	require.Zero(t, c.GetInt(ginKeyResilienceRetryAfter))
	require.True(t, resilience.RetryAdviceFrom(c.Request.Context()).Suppress)
	require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
	// The entry wrapper must recognize the typed error before a generic
	// no-account diagnosis can count a turn or populate legacy hints.
	entry, _ := turnCapContext(t, EndpointResponses)
	got := classifyOpenAICompatibleNoAccountErrorFromGin(entry, nil, nil, "model", "model", capabilityOnlySelectionFailure{})
	require.Equal(t, cls, got)
	require.False(t, entry.GetBool(ginKeyResilienceTurnCounted))
	require.Empty(t, entry.Writer.Header().Get("Retry-After"))
	require.Equal(t, "false", entry.Writer.Header().Get("x-should-retry"))
}

func TestCapabilityOnlyHandlerExhaustion(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.TurnCap.Failures = 100 })
	resetTurnCap(t)
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions, "/v1/messages"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", endpoint, mixed), func(t *testing.T) {
				c, w := turnCapContext(t, endpoint)
				ctx := resilience.WithCapabilityRequest(c.Request.Context(), "responses", false)
				c.Request = c.Request.WithContext(ctx)
				c.Header("Retry-After", "7")
				c.Set(ginKeyResilienceRetryAfter, 7)
				resilience.SetRetryAdvice(ctx, resilience.RetryAdvice{Seconds: 7})
				if mixed {
					resilience.NoteCapabilityAttempt(ctx, false)
				}
				failure := &service.UpstreamFailoverError{StatusCode: 400, Reason: service.GatewayFailureReason(resilience.ReasonCapabilityStreamOnly), NextAccountAction: service.NextAccountRetry}
				resilienceAttemptFailed(ctx, 1, failure)
				h := &OpenAIGatewayHandler{}
				if endpoint == "/v1/messages" {
					h.handleAnthropicFailoverExhausted(c, failure, false)
				} else {
					h.handleFailoverExhausted(c, failure, false)
				}
				require.Equal(t, 503, w.Code)
				if mixed {
					require.NotContains(t, w.Body.String(), "supports non-streaming requests")
					require.False(t, resilience.RetryAdviceFrom(ctx).Suppress)
				} else {
					require.Contains(t, w.Body.String(), "supports non-streaming requests")
					require.NotContains(t, w.Body.String(), "Please try again")
					require.Empty(t, w.Header().Get("Retry-After"))
					require.Equal(t, "false", w.Header().Get("x-should-retry"))
					require.True(t, resilience.RetryAdviceFrom(ctx).Suppress)
					require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
				}
			})
		}
	}
}
