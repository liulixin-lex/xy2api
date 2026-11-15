package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"strings"
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
		service.MarkOpsStreamError(c, errType, message, status)
		advice := responsesFailedRetryAdvice(c, errType, code)
		if advice.Suppress && advice.Source == "turn_cap" {
			code = codeServerIsOverloaded
		}
	}
	return service.WriteTargetProtocolError(c, protocol, status, errType, code, param, message)
}
