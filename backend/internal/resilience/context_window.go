package resilience

import (
	"strings"

	"github.com/tidwall/gjson"
)

// IsContextWindowError recognizes existing context-limit evidence in error fields.
func IsContextWindowError(upstreamMsg string, upstreamBody []byte) bool {
	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "context_too_large") || strings.Contains(lower, "context_length_exceeded") {
			return true
		}
		if strings.Contains(lower, "maximum context length") || strings.Contains(lower, "max context length") {
			return true
		}
		hasExceeded := strings.Contains(lower, "exceed") || strings.Contains(lower, "too large") || strings.Contains(lower, "too long")
		if strings.Contains(lower, "context window") && hasExceeded {
			return true
		}
		if strings.Contains(lower, "context length") && hasExceeded {
			return true
		}
		return strings.Contains(lower, "token limit") &&
			strings.Contains(lower, "context") &&
			hasExceeded
	}

	if match(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"message",
		"error.code",
		"response.error.code",
		"code",
	} {
		if match(gjson.GetBytes(upstreamBody, path).String()) {
			return true
		}
	}
	// Do not let echoed request content in a structured JSON error change the
	// retry/client-status classification. Plain-text upstream errors remain
	// supported by scanning the whole body only when it is not valid JSON.
	return !gjson.ValidBytes(upstreamBody) && match(string(upstreamBody))
}
