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
	if inboundIsChatCompletions(c) {
		return service.PreOutputProtocolChat
	}
	return service.PreOutputProtocolAnthropic
}

func inboundOpenAIErrorProtocol(c *gin.Context) string {
	if inboundIsChatCompletions(c) {
		return service.PreOutputProtocolChat
	}
	if c != nil && c.Request != nil && c.Request.URL != nil && strings.Contains(c.Request.URL.Path, "/messages") {
		return service.PreOutputProtocolAnthropic
	}
	return service.PreOutputProtocolResponses
}
