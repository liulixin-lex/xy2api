package service

import "strings"

// Consume an explicit terminal contract only. Repair eligibility and raw
// encrypted-content rejections remain owned by the existing M10 repair path.
func upstreamEncryptedTerminalRejection(body []byte) *openAIEncryptedContentRejectionError {
	code := openAIStreamErrorField(body, "code")
	if code.String() != "encrypted_content_rejected" {
		return nil
	}
	_, _, param, message := upstreamClientErrorFields(400, body,
		SanitizeUpstreamErrorMessage(strings.TrimSpace(openAIStreamErrorField(body, "message").String())))
	return &openAIEncryptedContentRejectionError{Param: param, Message: message}
}
