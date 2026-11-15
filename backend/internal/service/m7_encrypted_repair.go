package service

import (
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

const m7ResponsesWireBodyKey = "xy_m7_responses_wire_body"

func m7EncryptedRepairEligible(account *Account) bool {
	return account != nil && account.Type == AccountTypeAPIKey && account.Platform != PlatformGrok
}

func bindM7ResponsesWireBody(c *gin.Context, account *Account, req *http.Request) error {
	if !m7EncryptedRepairEligible(account) || c == nil || req == nil || req.GetBody == nil {
		return nil
	}
	reader, err := req.GetBody()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(reader)
	if err == nil {
		c.Set(m7ResponsesWireBodyKey, body)
	}
	return err
}

func (s *OpenAIGatewayService) handleM7EncryptedFailure(ctx context.Context, c *gin.Context, account *Account, payload []byte, status int, outputStarted, sse bool) error {
	if !m7EncryptedRepairEligible(account) || c == nil {
		return nil
	}
	value, exists := c.Get(m7ResponsesWireBodyKey)
	wire, ok := value.([]byte)
	if !exists || !ok {
		return nil
	}
	message := extractOpenAISSEErrorMessage(payload)
	if !isHTTPEncryptedContentFailure(wire, payload, message, status) {
		return nil
	}
	if repair := openAIRequestRepairFrom(c); repair != nil {
		repair.setBody(wire)
	}
	var failure error
	if outputStarted {
		failure = terminateHTTPEncryptedContent(ctx, c, account, wire, payload, message, status, true, sse)
	} else {
		failure = newHTTPEncryptedContentRepairSignal(ctx, c, account, wire, payload, message, status, sse)
	}
	if _, repairing := failure.(*openAIRequestRepairSignal); failure != nil && !repairing {
		s.recordOpenAIStreamUpstreamError(c, account, false, "", "terminal_rejection", payload, message, status)
	}
	return failure
}
