package service

import (
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// isOpenAIEncryptedContentRejectionPayload recognizes the structured error
// shapes emitted by HTTP responses and Responses stream events. The payload is
// only used for routing and classification; it is never persisted here.
func isOpenAIEncryptedContentRejectionPayload(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	for _, prefix := range []string{"error", "response.error"} {
		code := strings.TrimSpace(gjson.GetBytes(payload, prefix+".code").String())
		message := strings.TrimSpace(gjson.GetBytes(payload, prefix+".message").String())
		if resilience.IsEncryptedContentRejection(code, message) {
			return true
		}
		if message != "" && gjson.Valid(message) && resilience.IsEncryptedContentRejection(
			extractUpstreamErrorCode([]byte(message)), extractUpstreamErrorMessage([]byte(message)),
		) {
			return true
		}
	}
	// Some relays put a complete error object in error.message.
	code := extractUpstreamErrorCode(payload)
	message := extractUpstreamErrorMessage(payload)
	return resilience.IsEncryptedContentRejection(code, message)
}
