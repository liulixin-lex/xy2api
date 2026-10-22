package service

import (
	"context"
	"encoding/json"

	"github.com/gin-gonic/gin"
)

// writeOpenAIStreamRuleError answers an in-stream response.failed that
// matched an error passthrough rule before any output of an OpenAI
// Responses stream (native or passthrough). Before any commit it keeps the
// rule's status code and JSON body. Once heartbeats committed 200 it ends
// the stream with response.created and response.failed (one id, sequence 0
// and 1) carrying the rule's type and message; the rule's status decides a
// retry hint (RetryAdviceEligible). Review DELTA-P1-2, PM instruction 5.
func writeOpenAIStreamRuleError(ctx context.Context, c *gin.Context, status int, errType, message string) {
	// Stop a request-level heartbeat first so it cannot interleave.
	committed := StopOpenAICompactSSEKeepaliveCommitted(c)
	if !committed && !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		c.JSON(status, gin.H{
			"error": gin.H{
				"type":    errType,
				"message": message,
			},
		})
		return
	}
	MarkOpsStreamError(c, errType, message, status)
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"type": errType, "code": errType, "message": message}})
	if _, err := c.Writer.Write(preOutputTerminalFrame(ctx, PreOutputProtocolResponses, status, body)); err == nil {
		c.Writer.Flush()
	}
}
