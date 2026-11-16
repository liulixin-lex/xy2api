package handler

import (
	"encoding/json"
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
	if inboundIsResponses(c) {
		return service.PreOutputProtocolResponses
	}
	return ""
}

func writeHandlerProtocolError(c *gin.Context, protocol string, status int, errType, code, param, message string) bool {
	if service.ProtocolTerminalClaimed(c) {
		return false
	}
	if service.OpenAIImagesJSONKeepalivePresent(c) && service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) < 0 && service.ClaimProtocolTerminal(c) {
		errorBody := gin.H{"type": errType, "message": message}
		if code != "" {
			errorBody["code"] = code
		}
		c.JSON(status, gin.H{"error": errorBody})
		return true
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
	if protocol == "" {
		if c.Writer.Written() && strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
			if c.Request != nil && c.Request.Context().Err() != nil || service.GatewayDeliveryFailed(c) {
				return false
			}
			service.StopOpenAICompactSSEKeepaliveCommitted(c)
			if !service.ClaimProtocolTerminal(c) {
				return false
			}
			errorBody := gin.H{"type": errType, "message": message}
			if code != "" {
				errorBody["code"] = code
			}
			body, _ := json.Marshal(gin.H{"error": errorBody})
			service.MarkOpsGatewayTerminalStatus(c, status)
			service.MarkOpsStreamError(c, errType, message, status)
			_, _ = c.Writer.WriteString("event: error\ndata: " + string(body) + "\n\n")
			c.Writer.Flush()
			return true
		}
		protocol = service.PreOutputProtocolChat // The existing generic JSON envelope.
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
