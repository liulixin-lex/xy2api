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
