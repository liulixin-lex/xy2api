package service

import "github.com/gin-gonic/gin"

func (s *OpenAIGatewayService) stripHTTPEncryptedContentForAttempt(c *gin.Context, account *Account, body []byte, credentialVersion string) []byte {
	if lineage := s.beginOpenAIM3(c, body); lineage != nil && account != nil {
		body, _ = s.stripHTTPInvalidEncryptedContent(body, lineage.groupID, lineage.session, account.ID, credentialVersion)
	}
	return body
}
