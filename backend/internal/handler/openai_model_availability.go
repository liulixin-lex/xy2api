package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Model unavailability is terminal once candidates are exhausted. Preserve the
// upstream status and diagnosis without turning it into a retryable outage.
func handleModelUnavailableExhausted(c *gin.Context, failure *service.UpstreamFailoverError, streamStarted, openAIJSON bool, writeError func(int, string, bool)) bool {
	if !failure.IsOpenAIModelUnavailable() {
		return false
	}
	if c.GetBool(ginKeyResilienceFinalized) {
		return true
	}
	suppressResilienceRetryAdvice(c, "model_unavailable")
	message := strings.TrimSpace(service.ExtractUpstreamErrorMessage(failure.ResponseBody))
	if message == "" && !json.Valid(failure.ResponseBody) {
		message = strings.TrimSpace(string(failure.ResponseBody))
	}
	message = service.SanitizeUpstreamErrorMessage(message)
	message = (resilience.RetryAdvice{Suppress: true}).Message(message)
	if message == "" {
		message = "The requested model is unavailable"
	}
	service.SetOpsUpstreamError(c, failure.StatusCode, message, "")
	resilience.StateFrom(c.Request.Context()).SetRecoveryOutcome("terminal_rejection")
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) {
		streamStarted = true
	}
	if streamStarted && c.Writer.Size() > 0 && !gatewayStreamHasOnlyHeartbeats(c) {
		service.MarkOpsStreamError(c, "invalid_request_error", message, failure.StatusCode)
		return true
	}
	if openAIJSON && !streamStarted {
		if beginResilienceTerminal(c) {
			service.WriteOpenAIUpstreamClientError(c, failure.StatusCode, failure.ResponseBody, message)
		}
	} else {
		writeError(failure.StatusCode, message, streamStarted)
	}
	return true
}
