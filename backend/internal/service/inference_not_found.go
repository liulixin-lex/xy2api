package service

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// classifyInferenceUpstreamHTTP is only for model-bearing inference attempts.
// Only an empty, non-JSON, or strictly generic not-found response is evidence
// that the account cannot serve the model or endpoint. Keep explicit account,
// request, and structured resource classifications unchanged.
func classifyInferenceUpstreamHTTP(status int, headers http.Header, body []byte, message string) resilience.Result {
	result := classifyUpstreamHTTP(status, headers, body, message)
	if status != http.StatusNotFound {
		return result
	}
	if result.Category != resilience.CategoryTransient {
		return result
	}
	if openAIStreamRequestRejected(body, message) {
		return resilience.Result{Category: resilience.CategoryClientError, Reason: "inference_request_rejected"}
	}
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		if openAIStreamRequestRejected([]byte(object.Raw), message) {
			return resilience.Result{Category: resilience.CategoryClientError, Reason: "inference_request_rejected"}
		}
	}
	if !isGenericInferenceNotFound(body, message) {
		return result
	}
	return resilience.Result{Category: resilience.CategoryModelUnsupported, Reason: "status_404_inference"}
}

func isGenericInferenceNotFound(body []byte, message string) bool {
	trimmedBody := strings.TrimSpace(string(body))
	trimmedMessage := strings.TrimSpace(message)
	if trimmedBody == "" {
		return true
	}
	if !gjson.ValidBytes(body) {
		return true
	}
	var hasStructuredMessage bool
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		for _, path := range []string{"error.message", "response.error.message", "message"} {
			candidate := strings.TrimSpace(object.Get(path).String())
			if candidate == "" || gjson.Valid(candidate) {
				continue
			}
			hasStructuredMessage = true
			if isGenericNotFoundText(candidate) {
				return true
			}
		}
	}
	return !hasStructuredMessage && isGenericNotFoundText(trimmedMessage)
}

func isGenericNotFoundText(message string) bool {
	message = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	switch message {
	case "not found", "404", "endpoint not found":
		return true
	default:
		return false
	}
}
