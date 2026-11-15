//go:build unit

package service

import "testing"

func TestClassifyOpenAIResponsesTerminal(t *testing.T) {
	for _, tc := range []struct {
		event, data                   string
		seen, valid, success, failure bool
		reason                        string
		status                        int
	}{
		{"", "[DONE]", false, false, false, false, "missing_response_terminal", 0},
		{"", `{"type":"response.output_text.delta","delta":"error"}`, false, false, false, false, "", 0},
		{"response.completed", `{`, true, false, false, true, "malformed_json", 0},
		{"response.completed", `[]`, true, false, false, true, "invalid_envelope", 0},
		{"", `{"type":"response.completed"}`, true, false, false, true, "missing_response", 0},
		{"response.completed", `{"type":"response.failed","response":{"status":"failed"}}`, true, false, false, true, "type_status_conflict", 0},
		{"", `{"type":"response.completed","response":{"output":[]}}`, true, true, true, false, "", 0},
		{"response.done", `{"type":"response.completed","response":{"status":"completed"}}`, true, true, true, false, "", 0},
		{"response.canceled", `{"type":"response.cancelled","response":{"status":"canceled"}}`, true, true, false, true, "upstream_cancelled", 0},
		{"", `{"type":"response.done","response":{"status":"failed"}}`, true, true, false, true, "upstream_failed", 0},
		{"", `{"type":"response.done","response":{}}`, true, false, false, true, "missing_status", 0},
		{"", `{"type":"response.done","response":{"status":"future"}}`, true, false, false, true, "unknown_status", 0},
		{"", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, true, true, false, false, "max_output_tokens", 0},
		{"", `{"error":{"code":503}}`, true, true, false, true, "upstream_failed", 0},
		{"", `{"error":null}`, false, false, false, false, "", 0},
		{"", `{"error":{}}`, false, false, false, false, "", 0},
		{"response.output_text.delta", `{"error":{"code":503}}`, false, false, false, false, "", 0},
		{"error", `{"type":"proxy.upstream_error","status":429}`, true, true, false, true, "upstream_failed", 429},
		{"", `{"type":"proxy.upstream_error","status":429}`, true, false, false, true, "malformed_wrapped_error", 0},
		{"response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"503","status_code":503}}}`, true, true, false, true, "upstream_failed", 503},
		{"response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":503,"status_code":503}}}`, true, false, false, true, "malformed_wrapped_error", 0},
		{"response.completed", string([]byte{255}), true, false, false, true, "invalid_utf8", 0},
	} {
		got := ClassifyOpenAIResponsesTerminal(tc.event, []byte(tc.data))
		if got.Seen != tc.seen || got.Valid != tc.valid || got.Success != tc.success || got.Failure != tc.failure || got.Reason != tc.reason || got.WrappedStatus != tc.status {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}
