package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// HTTPContinuationError carries an existing HTTP continuation rejection.
type HTTPContinuationError struct {
	Status               int
	Code, Message, Param string
	Family               resilience.StateFamily
}

func (e *HTTPContinuationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func NewHTTPContinuationOwnerMismatchError() *HTTPContinuationError {
	return &HTTPContinuationError{Status: http.StatusBadRequest, Code: "previous_response_owner_mismatch", Param: "previous_response_id", Message: "previous_response_id is not available for this user", Family: resilience.StateContinuationOwnerMismatch}
}

func NewHTTPContinuationUnsupportedError() *HTTPContinuationError {
	return &HTTPContinuationError{Status: http.StatusBadRequest, Code: "http_continuation_unsupported", Param: "previous_response_id", Message: "The selected upstream does not support previous_response_id over HTTP; send complete input history", Family: resilience.StateHTTPContinuationUnsupported}
}

func NewToolOutputMissingCallIDError(index int, itemType string) *HTTPContinuationError {
	return &HTTPContinuationError{Status: http.StatusBadRequest, Code: "tool_output_missing_call_id", Param: fmt.Sprintf("input[%d].call_id", index), Message: fmt.Sprintf("input[%d] (%s) requires a non-empty string call_id; only a named function_call_output may omit call_id or use null", index, strings.TrimSpace(itemType)), Family: resilience.StateToolOutputMissingCallID}
}

func NewHTTPContinuationExpiredError() *HTTPContinuationError {
	return &HTTPContinuationError{Status: http.StatusGone, Code: "upstream_continuation_expired", Param: "previous_response_id", Message: "The upstream continuation state is no longer available; send complete input history", Family: resilience.StateUpstreamContinuationExpired}
}

// classifyHTTPContinuationError requires explicit continuation evidence. A
// generic 404/409, or a mere mention of previous state, is not enough.
func classifyHTTPContinuationError(payload []byte) *HTTPContinuationError {
	code := strings.ToLower(strings.TrimSpace(openAIStreamErrorField(payload, "code").String()))
	param := strings.TrimSpace(openAIStreamErrorField(payload, "param").String())
	message := strings.ToLower(openAIStreamErrorField(payload, "message").String())
	if param != "" && param != "previous_response_id" {
		return nil
	}
	switch code {
	case "previous_response_not_found", "previous_response_expired", "previous_response_id_not_found", "upstream_continuation_expired":
		return NewHTTPContinuationExpiredError()
	case "http_continuation_unsupported", "previous_response_id_unsupported", "unsupported_previous_response_id":
		return NewHTTPContinuationUnsupportedError()
	}
	if param == "previous_response_id" && (code == "unsupported_parameter" || code == "unknown_parameter") {
		return NewHTTPContinuationUnsupportedError()
	}
	if param == "previous_response_id" {
		if strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") {
			return NewHTTPContinuationUnsupportedError()
		}
		if strings.Contains(message, "expired") || strings.Contains(message, "not found") || strings.Contains(message, "no longer available") || strings.Contains(message, "does not exist") || strings.Contains(message, "no response found") {
			return NewHTTPContinuationExpiredError()
		}
	}
	message = strings.NewReplacer("'", "", `"`, "").Replace(message)
	if strings.Contains(message, "unsupported parameter: previous_response_id") {
		return NewHTTPContinuationUnsupportedError()
	}
	for _, subject := range []string{"previous_response_id", "previous response"} {
		for _, suffix := range []string{" is not supported", " is unsupported"} {
			if strings.Contains(message, subject+suffix) {
				return NewHTTPContinuationUnsupportedError()
			}
		}
		for _, suffix := range []string{" not found", " has expired", " is expired", " is no longer available", " does not exist"} {
			if strings.Contains(message, subject+suffix) {
				return NewHTTPContinuationExpiredError()
			}
		}
	}
	return nil
}

func writeHTTPContinuationError(c *gin.Context, rejection *HTTPContinuationError) bool {
	if rejection == nil {
		return false
	}
	suppressHTTPStateRejectionRetry(c)
	return WriteTargetProtocolError(c, openAIHTTPErrorProtocol(c), rejection.Status, "invalid_request_error", rejection.Code, rejection.Param, rejection.Message)
}

// WriteHTTPContinuationError records an existing local rejection and preserves
// its structured fields through the shared protocol writer.
func WriteHTTPContinuationError(c *gin.Context, account *Account, rejection *HTTPContinuationError) bool {
	if rejection == nil || ProtocolTerminalClaimed(c) {
		return false
	}
	noteLocalHTTPContinuationRejection(c, account, rejection)
	return writeHTTPContinuationError(c, rejection)
}
