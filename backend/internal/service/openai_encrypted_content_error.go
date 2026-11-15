package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// This is a terminal request error, never an account failover signal.
type openAIEncryptedContentRejectionError struct{ Param, Message string }

func (e *openAIEncryptedContentRejectionError) Error() string { return e.Message }

func openAIHTTPErrorProtocol(c *gin.Context) string {
	if c != nil && c.Request != nil && c.Request.URL != nil {
		switch {
		case strings.HasSuffix(c.Request.URL.Path, "/chat/completions"):
			return PreOutputProtocolChat
		case strings.HasSuffix(c.Request.URL.Path, "/messages"):
			return PreOutputProtocolAnthropic
		}
	}
	return PreOutputProtocolResponses
}

func suppressHTTPStateRejectionRetry(c *gin.Context) {
	if c == nil {
		return
	}
	if c.Request != nil {
		resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: "non_retryable"})
	}
	if c.Writer != nil && !c.Writer.Written() {
		c.Header("Retry-After", "")
		c.Header("x-should-retry", "false")
	}
}

func writeOpenAIEncryptedContentRejection(c *gin.Context, protocol string, rejection *openAIEncryptedContentRejectionError) bool {
	if rejection == nil {
		return false
	}
	suppressHTTPStateRejectionRetry(c)
	return WriteTargetProtocolError(c, protocol, http.StatusBadRequest, "invalid_request_error", "encrypted_content_rejected", rejection.Param, rejection.Message)
}

func isHTTPReasoningEncryptedRejection(body, payload []byte, message string) bool {
	if !isOpenAIEncryptedContentRejectionPayload(payload) && !resilience.IsEncryptedContentRejection("", message) {
		return false
	}
	// The nested M3 repair contract has its own state and budget. Do not turn
	// those requests into top-level reasoning/compaction failures.
	switch openAIStateRejection(payload, message).Family {
	case resilience.StateFunctionOutput, resilience.StateAgentMessage, resilience.StateRelay:
		return false
	case resilience.StateOtherEncrypted, resilience.StateConversationInvalid:
		input := gjson.GetBytes(body, "input")
		items := input.Array()
		if input.IsObject() {
			items = []gjson.Result{input}
		}
		for _, item := range items {
			if openAIEncryptedLineageItemType(item.Get("type").String()) {
				return true
			}
		}
		// A generic wrapped rejection of an existing nested M3 request must
		// retain its prior repair path, even without a named amsg_ locator.
		if slots, _, err := openAIM3Slots(body); err == nil && len(slots) > 0 {
			return false
		}
		return true
	default:
		return true
	}
}

// httpEncryptedNamedItem returns the single item id the upstream named.
func httpEncryptedNamedItem(payload []byte) string {
	named := ""
	add := func(id string) bool {
		if id == "" || id == named {
			return true
		}
		if named != "" {
			return false
		}
		named = id
		return true
	}
	if !add(openAIStreamErrorField(payload, "item_id").String()) {
		return ""
	}
	for _, field := range []string{"code", "param", "message"} {
		for _, match := range httpEncryptedItemPattern.FindAllStringSubmatch(openAIStreamErrorField(payload, field).String(), -1) {
			if !add(firstNonEmpty(match[1], match[2])) {
				return ""
			}
		}
	}
	return named
}

// selected must be the same selection the repair decision used, so a
// location found through the original request is not lost here.
func newOpenAIEncryptedContentRejection(ctx context.Context, account *Account, body, payload []byte, selected httpRejectedEncryptedItem) *openAIEncryptedContentRejectionError {
	// Project only error fields, using the existing credential scrubber before
	// exposing details. The original payload is still used for item selection.
	safe := redactOpenAITerminalOpsSensitiveBody(ctx, nil, account, payload)
	clean := func(text string) string {
		text = string(redactOpenAITerminalOpsSensitiveBody(ctx, nil, account, []byte(text)))
		input := gjson.GetBytes(body, "input")
		items := input.Array()
		if input.IsObject() {
			items = []gjson.Result{input}
		}
		for _, item := range items {
			if cipher := item.Get("encrypted_content"); cipher.Type == gjson.String && cipher.String() != "" {
				text = strings.ReplaceAll(text, cipher.String(), "[redacted]")
			}
		}
		return SanitizeUpstreamErrorMessage(text)
	}
	param := clean(openAIStreamErrorField(safe, "param").String())
	locator := ""
	if selected.ItemIndex >= 0 {
		if param != "" {
			locator = param
		} else if id := httpEncryptedNamedItem(payload); id != "" {
			locator = "item " + clean(id)
		}
	}
	message := "The upstream rejected an encrypted item in the request. The gateway could not identify a single item to repair safely. Rebuild the request from complete readable history, or start a new conversation."
	if locator != "" {
		message = "The upstream rejected encrypted content at " + locator + ". The gateway could not repair this item safely. Remove only the identified encrypted item and retry, or start a new conversation."
	}
	var details []string
	for _, field := range []string{"code", "message", "param", "item_id"} {
		if value := clean(openAIStreamErrorField(safe, field).String()); value != "" {
			details = append(details, field+": "+value)
		}
	}
	if len(details) > 0 {
		message += " Upstream detail: " + strings.Join(details, "; ")
	}
	return &openAIEncryptedContentRejectionError{Param: param, Message: message}
}
