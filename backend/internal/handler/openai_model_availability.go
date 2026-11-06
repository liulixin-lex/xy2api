package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Model unavailability and upstream request restrictions are terminal once
// candidates are exhausted or the cross-host guard establishes request scope.
func handleOpenAIRequestRejectionExhausted(c *gin.Context, failure *service.UpstreamFailoverError, streamStarted, openAIJSON bool, writeError func(int, string, bool)) bool {
	if !failure.IsOpenAIModelUnavailable() && !failure.IsOpenAIUpstreamRequestRestriction() {
		return false
	}
	if c.GetBool(ginKeyResilienceFinalized) {
		return true
	}
	source := "model_unavailable"
	if failure.IsOpenAIUpstreamRequestRestriction() {
		source = resilience.ReasonUpstreamRequestRestriction
		// This extension header only affects clients that recognize it. Codex
		// 0.166 ignores it and may retry 5xx within its own budget; the host
		// guard limits each retry to at most two upstream hosts.
		c.Header("x-should-retry", "false")
	}
	suppressResilienceRetryAdvice(c, source)
	message := strings.TrimSpace(service.ExtractUpstreamErrorMessage(failure.ResponseBody))
	if message == "" && !json.Valid(failure.ResponseBody) {
		message = strings.TrimSpace(string(failure.ResponseBody))
	}
	message = service.SanitizeUpstreamErrorMessage(message)
	message = (resilience.RetryAdvice{Suppress: true}).Message(message)
	if message == "" {
		message = "Upstream rejected the request"
		if failure.IsOpenAIModelUnavailable() {
			message = "The requested model is unavailable"
		}
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
