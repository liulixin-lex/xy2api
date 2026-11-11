package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Conversion failures describe client intent that this protocol cannot carry.
// Handle them before failover and account-health reporting, including errors
// wrapped by json.MarshalerError. A failed conversion never reached upstream.
func handleUnsupportedProtocolConversion(c *gin.Context, err error, protocol string, streamStarted bool) bool {
	var conversion *apicompat.UnsupportedConversionError
	if !errors.As(err, &conversion) {
		return false
	}
	stopPreOutputKeepalive(c)
	committed := service.StopOpenAICompactSSEKeepaliveCommitted(c)
	resilience.StateFrom(c.Request.Context()).SetRecoveryOutcome("terminal_rejection")
	if c.Writer.Written() && c.Writer.Status() >= http.StatusBadRequest {
		return true // A service already emitted this rejection.
	}
	message := conversion.Error()
	if streamStarted || committed || c.Writer.Written() {
		service.MarkOpsStreamError(c, "invalid_request_error", message, http.StatusBadRequest)
		switch protocol {
		case "responses":
			writeResponsesFailedSSEWithOpeningStatus(c, http.StatusBadRequest, "invalid_request_error", "unsupported_conversion", message, true)
		case "chat":
			writeChatCompletionsErrorSSE(c, "invalid_request_error", "unsupported_conversion", message, http.StatusBadRequest)
		default:
			if beginResilienceTerminal(c) {
				payload, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": message}})
				_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", payload)
				c.Writer.Flush()
			}
		}
		return true
	}
	if !beginResilienceTerminal(c) {
		return true
	}
	payload := gin.H{"error": gin.H{"type": "invalid_request_error", "code": "unsupported_conversion", "param": conversion.Field, "message": message}}
	if protocol == "messages" {
		payload["type"] = "error"
	}
	c.JSON(http.StatusBadRequest, payload)
	return true
}
