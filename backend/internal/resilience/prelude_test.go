package resilience

import "testing"

func TestClassifyStreamEvent(t *testing.T) {
	tests := []struct {
		name, protocol, event, payload string
		kind                           EventKind
		retry                          bool
	}{
		{"created", "responses", "", `{"type":"response.created","response":{"id":"resp_a"}}`, Prelude, false},
		{"queued", "responses", "response.queued", `{}`, Prelude, false},
		{"keepalive", "responses", "keepalive", `{}`, Prelude, false},
		{"unknown_metadata", "responses", "vendor.rate_limits", `{}`, Unknown, false},
		{"unknown_codex", "responses", "codex.unverified", `{}`, Unknown, false},
		{"empty_message", "responses", "response.output_item.added", `{"item":{"type":"message","content":[]}}`, Prelude, false},
		{"unknown_item_payload", "responses", "response.output_item.added", `{"item":{"type":"message","content":[],"vendor_payload":"opaque"}}`, Content, false},
		{"unknown_part_payload", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":"","annotation":{"value":"opaque"}}}`, Content, false},
		{"malformed_message_content", "responses", "response.output_item.added", `{"item":{"type":"message","content":"opaque"}}`, Content, false},
		{"phase_is_metadata", "responses", "response.output_item.added", `{"item":{"id":"msg_a","type":"message","status":"in_progress","role":"assistant","phase":"answer","content":[]}}`, Prelude, false},
		{"unknown_codex_metadata", "responses", "codex.response.metadata", `{}`, Unknown, false},
		{"empty_reasoning", "responses", "response.output_item.added", `{"item":{"type":"reasoning","summary":[]}}`, Prelude, false},
		{"encrypted_reasoning", "responses", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"secret"}}`, Content, false},
		{"empty_tool", "responses", "response.output_item.added", `{"item":{"type":"function_call","arguments":""}}`, Prelude, false},
		{"tool_arguments", "responses", "response.output_item.added", `{"item":{"type":"function_call","arguments":"{}"}}`, Content, false},
		{"tool_delta", "responses", "response.function_call_arguments.delta", `{"delta":"{}"}`, Content, false},
		{"empty_part", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":""}}`, Prelude, false},
		{"part_content", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":"hello"}}`, Content, false},
		{"missing_part", "responses", "response.content_part.added", `{}`, Unknown, false},
		{"empty_summary", "responses", "response.reasoning_summary_part.added", `{"part":{"type":"summary_text","text":""}}`, Prelude, false},
		{"text", "responses", "response.output_text.delta", `{"delta":"hi"}`, Content, false},
		{"refusal", "responses", "response.refusal.delta", `{"delta":"cannot"}`, Content, false},
		{"completed_empty", "responses", "response.completed", `{"response":{"output":[],"usage":{"input_tokens":1}}}`, TerminalSuccess, false},
		{"incomplete", "responses", "response.incomplete", `{"response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, TerminalFailure, false},
		{"overloaded", "responses", "response.failed", `{"response":{"error":{"code":"server_is_overloaded","message":"busy"}}}`, TerminalFailure, true},
		{"policy", "responses", "response.failed", `{"response":{"error":{"code":"content_policy_violation"}}}`, TerminalFailure, false},
		{"bad_request", "responses", "error", `{"error":{"type":"invalid_request_error","message":"bad input"}}`, TerminalFailure, false},
		{"busy", "responses", "error", `{"error":{"message":"The service is busy"}}`, TerminalFailure, true},
		{"malformed", "responses", "response.created", `{`, Unknown, false},
		{"chat_role", "chat", "", `{"object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}]}`, Prelude, false},
		{"chat_empty", "chat", "", `{"choices":[{"delta":{"content":""}}]}`, Prelude, false},
		{"chat_text", "chat", "", `{"choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_tool", "chat", "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a"}]}}]}`, Content, false},
		{"chat_unknown_delta", "chat", "", `{"choices":[{"delta":{"vendor":"value"}}]}`, Content, false},
		{"chat_finish", "chat", "", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, TerminalSuccess, false},
		{"chat_usage_only", "chat", "", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0}}`, TerminalSuccess, false},
		{"chat_error_before_usage", "chat", "", `{"error":{"code":"server_is_overloaded"},"usage":{"prompt_tokens":1}}`, TerminalFailure, true},
		{"chat_done", "chat", "", `[DONE]`, TerminalSuccess, false},
		{"messages_start", "messages", "message_start", `{"message":{"id":"msg_a"}}`, Prelude, false},
		{"messages_ping", "messages", "ping", `{}`, Prelude, false},
		{"messages_empty_text", "messages", "content_block_start", `{"content_block":{"type":"text","text":""}}`, Prelude, false},
		{"messages_empty_thinking", "messages", "content_block_start", `{"content_block":{"type":"thinking","thinking":""}}`, Prelude, false},
		{"messages_tool", "messages", "content_block_start", `{"content_block":{"type":"tool_use","id":"tool_a","input":{}}}`, Content, false},
		{"messages_delta", "messages", "content_block_delta", `{"delta":{"type":"text_delta","text":"hi"}}`, Content, false},
		{"messages_stop", "messages", "message_stop", `{}`, TerminalSuccess, false},
		{"messages_error", "messages", "error", `{"error":{"type":"overloaded_error"}}`, TerminalFailure, true},
		{"unknown_protocol", "vendor", "message_start", `{}`, Unknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyStreamEvent(tt.protocol, tt.event, []byte(tt.payload))
			if got.Kind != tt.kind || got.Retryable != tt.retry {
				t.Fatalf("got %+v, want kind=%v retry=%v", got, tt.kind, tt.retry)
			}
		})
	}
}
