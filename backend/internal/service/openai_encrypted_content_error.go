package service

import (
	"context"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// This is a terminal request error, never an account failover signal.
type openAIEncryptedContentRejectionError struct{ Param, Message string }

func (e *openAIEncryptedContentRejectionError) Error() string { return e.Message }

func isHTTPReasoningEncryptedRejection(payload []byte, message string) bool {
	if !isOpenAIEncryptedContentRejectionPayload(payload) && !resilience.IsEncryptedContentRejection("", message) {
		return false
	}
	// The nested M3 repair contract has its own state and budget. Do not turn
	// those requests into top-level reasoning/compaction failures.
	switch openAIStateRejection(payload, message).Family {
	case resilience.StateConversationInvalid, resilience.StateFunctionOutput, resilience.StateAgentMessage, resilience.StateRelay:
		return false
	default:
		return true
	}
}

func newOpenAIEncryptedContentRejection(ctx context.Context, account *Account, body, payload []byte) *openAIEncryptedContentRejectionError {
	selected, _ := selectHTTPRejectedEncryptedItem(body, payload)
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
		} else if selected.ItemID != "" {
			raw := openAIStreamErrorField(payload, "message").String()
			if strings.Contains(raw, selected.ItemID) || openAIStreamErrorField(payload, "item_id").String() == selected.ItemID {
				locator = "item " + clean(selected.ItemID)
			}
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
