package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func inboundErrorProtocol(c *gin.Context) string {
	if inboundIsResponses(c) {
		return service.PreOutputProtocolResponses
	}
	if resilienceChatTerminalFrame(c) {
		return service.PreOutputProtocolChat
	}
	return service.PreOutputProtocolAnthropic
}

func inboundOpenAIErrorProtocol(c *gin.Context) string {
	if resilienceChatTerminalFrame(c) {
		return service.PreOutputProtocolChat
	}
	if c != nil && c.Request != nil && c.Request.URL != nil && strings.Contains(c.Request.URL.Path, "/messages") {
		return service.PreOutputProtocolAnthropic
	}
	return service.PreOutputProtocolResponses
}

func writeHandlerProtocolError(c *gin.Context, protocol string, status int, errType, code, param, message string) bool {
	if service.ProtocolTerminalClaimed(c) {
		return false
	}
	if protocol == service.PreOutputProtocolResponses && c.Request != nil && c.Writer.Written() && strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		code = mapResponsesErrorCode(errType, code)
		service.MarkOpsStreamError(c, errType, message, status)
		if resilience.Current().FailedFrameRetryAfterOn() {
			advice := responsesFailedRetryAdvice(c, errType, code)
			if advice.Suppress && advice.Source == "turn_cap" {
				code = codeServerIsOverloaded
			}
		}
	}
	return service.WriteTargetProtocolError(c, protocol, status, errType, code, param, message)
}

// Explicit SSE wrappers retain their original contract when called before the
// first event. General errors still choose the actual committed response type.
func prepareHandlerErrorSSE(c *gin.Context) bool {
	if c == nil || c.Writer == nil || service.ProtocolTerminalClaimed(c) {
		return false
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		return false
	}
	if !c.Writer.Written() {
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeaderNow()
	}
	return true
}
