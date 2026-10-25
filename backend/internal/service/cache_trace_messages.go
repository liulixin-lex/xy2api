package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// The bridge is an independently selectable observation platform, while its
// emitted events and aggregate partition remain OpenAI.
func cacheTraceOpenAIPlatform(c *gin.Context) string {
	if c != nil && c.Request != nil && c.Request.URL != nil &&
		strings.HasSuffix(strings.TrimSuffix(c.Request.URL.Path, "/"), "/messages") {
		return "messages_bridge"
	}
	return PlatformOpenAI
}

// The existing protocol resolver owns routing. Only its Responses bridge is
// covered here; raw Chat and native Anthropic subpaths keep their old behavior.
func cacheTraceMessagesAttemptAllowed(c *gin.Context) bool {
	return cacheTraceOpenAIPlatform(c) != "messages_bridge" ||
		GetActualOpenAIUpstreamEndpoint(c) == openAIResponsesUpstreamEndpoint
}
