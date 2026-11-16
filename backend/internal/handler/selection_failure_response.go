package handler

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Keep the classifier's request field and code through the existing writer.
func (h *OpenAIGatewayHandler) handleOpenAISelectionError(c *gin.Context, cls noAccountErrorClassification, streamStarted bool) {
	var failure *service.UpstreamFailoverError
	if errors.As(cls.LastCause, &failure) {
		h.handleFailoverExhausted(c, failure, streamStarted)
		return
	}
	if cls.Code != "responses_required" {
		h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
		return
	}
	if streamStarted && !c.Writer.Written() {
		prepareHandlerErrorSSE(c)
	}
	applyConcurrencyRetryHint(c, cls.Status, cls.Code)
	errType, code, message := resilienceTranslateError(c, cls.Status, cls.ErrType, cls.Code, cls.Message)
	writeHandlerProtocolError(c, inboundOpenAIErrorProtocol(c), cls.Status, errType, code, cls.Param, message)
}
