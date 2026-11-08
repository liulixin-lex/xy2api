package service

import (
	"net/http"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// classifyInferenceUpstreamHTTP is only for model-bearing inference attempts.
// A 404 without request-resource evidence means this account cannot serve the
// model or endpoint. Keep the general HTTP classifier unchanged for token
// counting, resource retrieval and other non-inference calls.
func classifyInferenceUpstreamHTTP(status int, headers http.Header, body []byte, message string) resilience.Result {
	result := classifyUpstreamHTTP(status, headers, body, message)
	if status != http.StatusNotFound {
		return result
	}
	if result.Category == resilience.CategoryClientError && result.Reason == "client_code" {
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
	if inferenceNotFoundReferencesClientResource(body, message) {
		return resilience.Result{Category: resilience.CategoryClientError, Reason: "request_resource_not_found"}
	}
	return resilience.Result{Category: resilience.CategoryModelUnsupported, Reason: "status_404_inference"}
}

func inferenceNotFoundReferencesClientResource(body []byte, message string) bool {
	// Inspect the same bounded error envelopes as model availability, including
	// one embedded relay error. A non-model parameter identifies request input.
	if !openAIModelAvailabilityParamAllowed(body) {
		return true
	}
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		for _, path := range []string{"error.code", "response.error.code", "code"} {
			switch strings.ToLower(strings.TrimSpace(object.Get(path).String())) {
			case "previous_response_not_found", "response_not_found", "conversation_not_found",
				"file_not_found", "vector_store_not_found", "resource_not_found",
				"assistant_not_found", "thread_not_found", "run_not_found", "item_not_found":
				return true
			}
		}
		for _, path := range []string{"error.message", "response.error.message", "message"} {
			if inferenceNotFoundResourceMessage(object.Get(path).String()) {
				return true
			}
		}
	}
	return inferenceNotFoundResourceMessage(message) || (!gjson.ValidBytes(body) && inferenceNotFoundResourceMessage(string(body)))
}

func inferenceNotFoundResourceMessage(message string) bool {
	message = strings.ToLower(message)
	for _, prefix := range []string{"no response found with id", "no file found with id", "no conversation found with id", "no item found with id"} {
		if strings.Contains(message, prefix) {
			return true
		}
	}
	return false
}
