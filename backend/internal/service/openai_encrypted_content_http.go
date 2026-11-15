package service

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func (s *OpenAIGatewayService) stripHTTPEncryptedContentForAttempt(c *gin.Context, account *Account, body []byte, credentialVersion string) []byte {
	if lineage := s.beginOpenAIM3(c, body); lineage != nil && account != nil {
		body, _ = s.stripHTTPInvalidEncryptedContent(body, lineage.groupID, lineage.session, account.ID, credentialVersion)
	}
	return body
}

func isHTTPEncryptedContentFailure(body, payload []byte, message string, status int) bool {
	return (status == 0 || status == http.StatusBadRequest || status == http.StatusBadGateway) && isHTTPReasoningEncryptedRejection(body, payload, message)
}

// httpEncryptedRepairPlan is one same-account replay for an encrypted
// rejection; Selected is the selection the terminal error also reports.
type httpEncryptedRepairPlan struct {
	Selected httpRejectedEncryptedItem
	Next     []byte
	Digests  []string
	Bulk     bool
}

// planHTTPEncryptedContentRepair consumes the shared in-place budget only
// when it returns true. The first rejection repairs the uniquely located
// item. A further rejection that does not point at an item already cleared
// shows more than one item failed, so the remaining top-level ciphertext is
// cleared once, as the earlier whole-request repair did.
func planHTTPEncryptedContentRepair(ctx context.Context, c *gin.Context, account *Account, r *openAIRequestRepair, body, payload []byte) (httpEncryptedRepairPlan, bool) {
	selected, err := openAIRequestEncryptedContentRepairBody(c, body, payload)
	plan := httpEncryptedRepairPlan{Selected: selected}
	if err != nil || r == nil || account == nil || r.accountID != account.ID || r.encryptedBulkTried || ctx.Err() != nil {
		return plan, false
	}
	if !r.encryptedTried {
		if !selected.Changed {
			return plan, false
		}
		plan.Next, plan.Digests = selected.Body, []string{selected.Digest}
	} else {
		if selected.ItemIndex >= 0 && !selected.Changed {
			return plan, false
		}
		next, digests, err := stripHTTPTopLevelEncryptedItems(body)
		if err != nil || len(digests) == 0 {
			return plan, false
		}
		plan.Next, plan.Digests, plan.Bulk = next, digests, true
	}
	if !resilience.StateFrom(ctx).Budget().ConsumeInPlaceRetry(account.ID, false, time.Now()) {
		return plan, false
	}
	return plan, true
}

func (r *openAIRequestRepair) noteHTTPEncryptedRepair(plan httpEncryptedRepairPlan) {
	if r != nil {
		r.encryptedTried = true
		r.encryptedBulkTried = r.encryptedBulkTried || plan.Bulk
	}
}

// Both HTTP and buffered/pre-output SSE use this same repair budget.
func newHTTPEncryptedContentRepairSignal(ctx context.Context, c *gin.Context, account *Account, body, payload []byte, message string, status int, sse bool) error {
	if !isHTTPEncryptedContentFailure(body, payload, message, status) {
		return nil
	}
	if plan, ok := planHTTPEncryptedContentRepair(ctx, c, account, openAIRequestRepairFrom(c), body, payload); ok {
		return &openAIRequestRepairSignal{kind: resilience.ReasonEncryptedContent, next: plan.Next, encrypted: plan}
	}
	return terminateHTTPEncryptedContent(ctx, c, account, body, payload, message, status, false, sse)
}

func terminateHTTPEncryptedContent(ctx context.Context, c *gin.Context, account *Account, body, payload []byte, message string, status int, outputStarted, sse bool) error {
	if !isHTTPEncryptedContentFailure(body, payload, message, status) {
		return nil
	}
	path := "native_http"
	if c != nil && c.GetBool("openai_passthrough") {
		path = "passthrough_http"
	}
	if sse || outputStarted {
		path = "native_sse"
		if c != nil && c.GetBool("openai_passthrough") {
			path = "passthrough_sse"
		}
	}
	captureOpenAIStateRejection(c, account, payload, message, path, outputStarted, body, status)
	rejection := newOpenAIEncryptedContentRejection(ctx, account, body, payload)
	writeOpenAIEncryptedContentRejection(c, openAIHTTPErrorProtocol(c), rejection)
	return &OpenAIRequestRejectionError{rejection}
}

func openAIHTTPRepairBody(c *gin.Context) []byte {
	if r := openAIRequestRepairFrom(c); r != nil {
		return r.body
	}
	if c != nil {
		body, _ := c.Get(openAIStateRejectionBodyKey)
		if body, ok := body.([]byte); ok {
			return body
		}
	}
	return nil
}

func httpEncryptedRepairContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}

// Flush already accepted semantic bytes before writing a single failure. The
// failing upstream frame itself has not entered the downstream buffer yet.
func finishHTTPEncryptedContentAfterOutput(ctx context.Context, c *gin.Context, account *Account, payload []byte, message string, status int, flush func() error) error {
	body := openAIHTTPRepairBody(c)
	if !isHTTPEncryptedContentFailure(body, payload, message, status) {
		return nil
	}
	if flush != nil {
		if err := flush(); err != nil {
			return err
		}
	}
	return terminateHTTPEncryptedContent(ctx, c, account, body, payload, message, status, true, true)
}

func (s *OpenAIGatewayService) handleHTTPEncryptedContentFrame(ctx context.Context, c *gin.Context, account *Account, payload []byte, status int, outputStarted bool, flush func() error, upstreamRequestID string) error {
	message := extractOpenAISSEErrorMessage(payload)
	if outputStarted {
		rejection := finishHTTPEncryptedContentAfterOutput(ctx, c, account, payload, message, status, flush)
		if rejection != nil && isHTTPEncryptedContentFailure(openAIHTTPRepairBody(c), payload, message, status) {
			s.recordOpenAIStreamUpstreamError(c, account, c != nil && c.GetBool("openai_passthrough"), upstreamRequestID, "stream_failed", payload, message, status)
		}
		return rejection
	}
	return newHTTPEncryptedContentRepairSignal(ctx, c, account, openAIHTTPRepairBody(c), payload, message, status, true)
}
