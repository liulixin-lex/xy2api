package service

import (
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
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
	repair := openAIRequestRepairFrom(c)
	repair.setBody(wire)
	var selected httpRejectedEncryptedItem
	if outputStarted {
		// Selection retains the locator; do not consume a replay budget after output.
		selected, _ = openAIRequestEncryptedContentRepairBody(c, wire, payload)
	} else {
		plan, replay := planHTTPEncryptedContentRepair(ctx, c, account, repair, wire, payload)
		if replay {
			// AfterSignal records the same plan once, including bulk state and digests.
			return &openAIRequestRepairSignal{kind: resilience.ReasonEncryptedContent, next: plan.Next, encrypted: plan}
		}
		selected = plan.Selected
	}
	// M10 forwards this exact selection to its final rejection constructor.
	failure := terminateHTTPEncryptedContentWith(ctx, c, account, wire, payload, message, status, outputStarted, sse, selected)
	if failure != nil {
		s.recordOpenAIStreamUpstreamError(c, account, false, "", "terminal_rejection", payload, message, status)
	}
	return failure
}

// Keep the converted original before builder normalization and any replay. The
// M10 constructor needs both removed ciphertext and original item identities;
// an inbound Messages body contains signatures rather than Responses input.
func (s *OpenAIGatewayService) prepareM7EncryptedRepair(c *gin.Context, account *Account, repair *openAIRequestRepair, responsesBody []byte, token string) {
	if !m7EncryptedRepairEligible(account) || repair == nil {
		return
	}
	repair.credentialVersion = s.UpstreamCredentialVersion(account, token)
	repair.credentialRepo = s.accountRepo
	c.Set(openAIStateRejectionBodyKey, append([]byte(nil), responsesBody...))
}
