package service

import (
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
	// Match classifyUpstreamOutcome's value sources so routing and ops categories agree.
	return resilience.IsEncryptedContentRejection(extractUpstreamErrorCode(payload), extractUpstreamErrorMessage(payload)) ||
		resilience.IsEncryptedContentRejection(
			gjson.GetBytes(payload, "response.error.code").String(),
			gjson.GetBytes(payload, "response.error.message").String(),
		)
}

// openAIStateRejection uses the structured error envelope before the fallback
// message. A nonempty code must never be discarded to enable text matching.
func openAIStateRejection(payload []byte, message string) resilience.StateRejection {
	code := extractUpstreamErrorCode(payload)
	msg := extractUpstreamErrorMessage(payload)
	if gjson.GetBytes(payload, "response.error").IsObject() {
		code = firstNonEmpty(code, gjson.GetBytes(payload, "response.error.code").String())
		msg = firstNonEmpty(msg, gjson.GetBytes(payload, "response.error.message").String())
	}
	if rej := resilience.ClassifyStateRejection(code, msg); rej.RequestScoped() {
		return rej
	}
	// The stream reader may have extracted a message from another supported
	// envelope. Keep its code when considering that message, as the v3 guard did.
	return resilience.ClassifyStateRejection(code, message)
}

func isOpenAIStateRejection(payload []byte, message string) bool {
	return openAIStateRejection(payload, message).RequestScoped()
}
