package resilience

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClassifyStreamEvent(t *testing.T) {
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	Publish(Defaults())
	tests := []struct {
		name, protocol, event, payload string
		kind                           EventKind
		retry                          bool
	}{
		{"created", "responses", "", `{"type":"response.created","response":{"id":"resp_a"}}`, Prelude, false},
		{"queued", "responses", "response.queued", `{}`, Prelude, false},
		{"keepalive", "responses", "keepalive", `{}`, Prelude, false},
		{"unknown_metadata_ready", "responses", "vendor.rate_limits", `{}`, Prelude, false},
		{"unknown_codex_ready", "responses", "codex.unverified", `{}`, Prelude, false},
		{"empty_message", "responses", "response.output_item.added", `{"item":{"type":"message","role":"assistant","content":[]}}`, Prelude, false},
		{"unknown_item_payload", "responses", "response.output_item.added", `{"item":{"type":"message","content":[],"vendor_payload":"opaque"}}`, Content, false},
		{"unknown_part_payload", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":"","annotation":{"value":"opaque"}}}`, Content, false},
		{"malformed_message_content", "responses", "response.output_item.added", `{"item":{"type":"message","content":"opaque"}}`, Content, false},
		{"phase_is_metadata", "responses", "response.output_item.added", `{"item":{"id":"msg_a","type":"message","status":"in_progress","role":"assistant","phase":"answer","content":[]}}`, Prelude, false},
		{"codex_metadata", "responses", "codex.response.metadata", `{}`, Prelude, false},
		{"response_metadata", "responses", "response.metadata", `{"type":"response.metadata","sequence_number":2,"metadata":{"openai_chatgpt_moderation_metadata":{}}}`, Prelude, false},
		{"response_metadata_type_only", "responses", "", `{"type":"response.metadata","metadata":{}}`, Prelude, false},
		{"codex_rate_limits", "responses", "codex.rate_limits", `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":12}}}`, Prelude, false},
		{"codex_metadata_payload_type_wins", "responses", "response.metadata", `{"type":"codex.response.metadata"}`, Prelude, false},
		{"response_metadata_not_messages", "messages", "response.metadata", `{}`, Unknown, false},
		{"empty_reasoning", "responses", "response.output_item.added", `{"item":{"type":"reasoning","summary":[]}}`, Prelude, false},
		{"encrypted_reasoning", "responses", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"secret"}}`, Content, false},
		{"empty_tool", "responses", "response.output_item.added", `{"item":{"type":"function_call","arguments":""}}`, Prelude, false},
		{"tool_arguments", "responses", "response.output_item.added", `{"item":{"type":"function_call","arguments":"{}"}}`, Content, false},
		{"tool_delta", "responses", "response.function_call_arguments.delta", `{"delta":"{}"}`, Content, false},
		{"empty_part", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":""}}`, Prelude, false},
		{"part_content", "responses", "response.content_part.added", `{"part":{"type":"output_text","text":"hello"}}`, Content, false},
		{"missing_part", "responses", "response.content_part.added", `{}`, Content, false},
		{"empty_summary", "responses", "response.reasoning_summary_part.added", `{"part":{"type":"summary_text","text":""}}`, Prelude, false},
		{"text", "responses", "response.output_text.delta", `{"delta":"hi"}`, Content, false},
		{"refusal", "responses", "response.refusal.delta", `{"delta":"cannot"}`, Content, false},
		{"completed_empty", "responses", "response.completed", `{"response":{"output":[],"usage":{"input_tokens":1}}}`, TerminalSuccess, false},
		{"incomplete", "responses", "response.incomplete", `{"response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, TerminalFailure, false},
		{"overloaded", "responses", "response.failed", `{"response":{"error":{"code":"server_is_overloaded","message":"busy"}}}`, TerminalFailure, true},
		{"policy", "responses", "response.failed", `{"response":{"error":{"code":"content_policy_violation"}}}`, TerminalFailure, false},
		{"bad_request", "responses", "error", `{"error":{"type":"invalid_request_error","message":"bad input"}}`, TerminalFailure, false},
		{"busy", "responses", "error", `{"error":{"message":"The service is busy"}}`, TerminalFailure, true},
		{"malformed", "responses", "response.created", `{`, Content, false},
		{"blank_delta", "responses", "response.output_text.delta", `{"type":"response.output_text.delta","delta":" \t"}`, Prelude, false},
		{"empty_delta", "responses", "response.output_text.delta", `{"type":"response.output_text.delta","delta":""}`, Prelude, false},
		{"missing_delta", "responses", "response.output_text.delta", `{"type":"response.output_text.delta"}`, Content, false},
		{"blank_text_done", "responses", "response.output_text.done", `{"type":"response.output_text.done","text":"　 "}`, Prelude, false},
		{"assistant_item_done_blank", "responses", "response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":" "}]}}`, Prelude, false},
		{"empty_refusal_is_visible", "responses", "response.content_part.added", `{"type":"response.content_part.added","part":{"type":"refusal","refusal":""}}`, Content, false},
		{"non_text_part_is_visible", "responses", "response.content_part.added", `{"type":"response.content_part.added","part":{"type":"image","data":""}}`, Content, false},
		{"completed_always_terminal", "responses", "response.completed", `{"type":"response.completed","response":{"output":[]}}`, TerminalSuccess, false},
		{"chat_role", "chat", "", `{"object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}]}`, Prelude, false},
		{"chat_empty", "chat", "", `{"choices":[{"delta":{"content":""}}]}`, Prelude, false},
		{"chat_text", "chat", "", `{"choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_null_error", "chat", "", `{"error":null,"choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_empty_string_error", "chat", "", `{"error":"","choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_false_error", "chat", "", `{"error":false,"choices":[{"delta":{"role":"assistant"}}]}`, Prelude, false},
		{"chat_empty_object_error", "chat", "", `{"error":{},"choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_message_error", "chat", "", `{"error":{"message":"The service is busy"}}`, TerminalFailure, true},
		{"chat_blank_string_error", "chat", "", `{"error":" ","choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_zero_error", "chat", "", `{"error":0,"choices":[{"delta":{"role":"assistant"}}]}`, Prelude, false},
		{"chat_empty_fields_error", "chat", "", `{"error":{"message":"","code":"","type":""},"choices":[{"delta":{"content":"hi"}}]}`, Content, false},
		{"chat_string_error", "chat", "", `{"error":"The service is busy"}`, TerminalFailure, true},
		{"chat_type_error", "chat", "", `{"error":{"type":"server_error"}}`, TerminalFailure, true},
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

// Recursive null, string and container handling remains compatible with v0.5.4.
// Scalar metadata follows the separate G2c rule tested below.
func TestResponsesExtraFieldsMatchV054RecursiveEmptiness(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		empty       bool
	}{
		{"null", `null`, true},
		{"empty string", `""`, true},
		{"empty array", `[]`, true},
		{"empty object", `{}`, true},
		{"nested empty object", `{"x":{"y":""}}`, true},
		{"nested empty array", `{"x":[{}]}`, true},
		{"nested empty mixed array", `{"x":[null,""]}`, true},
		{"nested nonempty string", `{"x":{"y":"a"}}`, false},
		{"nested whitespace", `{"x":" "}`, false},
		{"no structural field exemption", `{"x":{"id":"r"}}`, false},
		{"no tool identity exemption", `{"x":{"type":"function_call","name":"tool","arguments":""}}`, false},
		{"duplicate cannot hide content", `{"x":{"y":"a","y":""}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := gjson.Parse(tc.value)
			legacy := emptyPreludeFieldsV054(value, false)
			if legacy != tc.empty {
				t.Fatalf("v0.5.4 empty=%t, want %t", legacy, tc.empty)
			}
			if got := responsesFieldValueIsEmpty(value); got != legacy {
				t.Errorf("empty=%t, v0.5.4=%t", got, legacy)
			}
			payload := fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[],"extra":%s}}`, tc.value)
			got := ResponsesEventOutputDisposition("response.output_item.added", []byte(payload))
			if !got.Matched || (got.Kind == Prelude) != legacy || got.Visible != !legacy || got.SemanticOutput != !legacy {
				t.Errorf("got %+v, v0.5.4 extra field empty=%t", got, legacy)
			}
		})
	}
}

func TestResponsesExtraFieldScalarsDoNotCountAsContent(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		empty       bool
	}{
		{"true", `true`, true},
		{"false", `false`, true},
		{"zero", `0`, true},
		{"number", `3`, true},
		{"negative number", `-4`, true},
		{"fractional number", `1.25`, true},
		{"exponent number", `1e100`, true},
		{"nested zero", `{"x":[0]}`, true},
		{"nested false", `{"x":false}`, true},
		{"nested scalars", `{"x":[true,3]}`, true},
		{"empty scalar containers", `{"x":[],"y":{}}`, true},
		{"nonempty string", `"y"`, false},
		{"whitespace string", `" "`, false},
		{"nested nonempty string", `{"x":{"y":"y"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[],"extra":%s}}`, tc.value)
			got := ResponsesEventOutputDisposition("response.output_item.added", []byte(payload))
			wantKind := Content
			if tc.empty {
				wantKind = Prelude
			}
			if !got.Matched || got.Kind != wantKind || got.Visible != !tc.empty || got.SemanticOutput != !tc.empty {
				t.Errorf("got %+v, want kind=%v empty=%t", got, wantKind, tc.empty)
			}
		})
	}
}

func TestResponsesExtraFieldDepthLimit(t *testing.T) {
	for _, tc := range []struct {
		depth     int
		wantEmpty bool
	}{
		{32, true},
		{33, false},
	} {
		t.Run(fmt.Sprintf("depth_%d", tc.depth), func(t *testing.T) {
			// Count array/object levels within the extra value, excluding the
			// event and item envelopes. Check both scalar and empty-container
			// leaves so a childless container cannot bypass the depth limit.
			for _, leaf := range []string{`null`, `{}`, `true`, `3`} {
				value := leaf
				levels := 0
				if leaf == `{}` {
					levels = 1
				}
				for ; levels < tc.depth; levels++ {
					if levels%2 == 0 {
						value = `{"x":` + value + `}`
					} else {
						value = `[` + value + `]`
					}
				}
				if got := responsesFieldValueIsEmpty(gjson.Parse(value)); got != tc.wantEmpty {
					t.Errorf("leaf=%s empty=%t, want %t", leaf, got, tc.wantEmpty)
				}
				payload := fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[],"extra":%s}}`, value)
				wantKind := Content
				if tc.wantEmpty {
					wantKind = Prelude
				}
				got := ResponsesEventOutputDisposition("response.output_item.added", []byte(payload))
				if !got.Matched || got.Kind != wantKind || got.Visible != !tc.wantEmpty || got.SemanticOutput != !tc.wantEmpty {
					t.Errorf("leaf=%s got %+v, want kind=%v empty=%t", leaf, got, wantKind, tc.wantEmpty)
				}
			}
		})
	}
}

// Frozen from v0.5.4:backend/internal/resilience/prelude.go, with only the
// function name changed. Extra fields use structural=false: field names never
// exempt a nonempty value. Keep this reference independent of the new helper.
func emptyPreludeFieldsV054(value gjson.Result, structural bool) bool {
	switch {
	case value.Type == gjson.Null:
		return true
	case value.Type == gjson.String:
		return value.String() == ""
	case value.IsArray():
		for _, entry := range value.Array() {
			if !emptyPreludeFieldsV054(entry, structural) {
				return false
			}
		}
		return true
	case value.IsObject():
		empty := true
		value.ForEach(func(key, child gjson.Result) bool {
			if structural {
				switch key.String() {
				case "id", "type", "status", "role", "index", "phase":
					return true
				case "call_id", "name":
					if typ := value.Get("type").String(); typ == "function_call" || typ == "custom_tool_call" {
						return true
					}
				}
			}
			nestedStructure := structural && (key.String() == "content" || key.String() == "summary")
			if !emptyPreludeFieldsV054(child, nestedStructure) {
				empty = false
				return false
			}
			return true
		})
		return empty
	default:
		return false
	}
}

func TestResponsesEventOutputDisposition(t *testing.T) {
	tests := []struct {
		name           string
		event          string
		payload        string
		placeholder    bool
		visible        bool
		semanticOutput bool
	}{
		{name: "blank delta", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","delta":"  "}`, placeholder: true},
		{name: "whitespace item", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"id":"m1","type":"message","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":"\n　"}]}}`, placeholder: true},
		{name: "delta logprobs obfuscation", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","delta":" ","logprobs":[],"obfuscation":"x"}`, placeholder: true},
		{name: "done logprobs", event: "response.output_text.done", payload: `{"type":"response.output_text.done","text":" ","logprobs":[]}`, placeholder: true},
		{name: "reasoning null", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"id":"r","type":"reasoning","summary":[],"encrypted_content":null}}`, placeholder: true},
		{name: "known summary cannot use recursive emptiness", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[{}],"extra":{"x":[null,""]}}}`, visible: true, semanticOutput: true},
		{name: "known encrypted content keeps wire type", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[],"encrypted_content":{"x":""},"extra":{"x":[{}]}}}`, visible: true, semanticOutput: true},
		{name: "known duplicate summary stays visible", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[],"summary":[],"extra":{"x":{"y":""}}}}`, visible: true, semanticOutput: true},
		{name: "duplicate type", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","type":"response.output_text.delta","delta":" "}`, visible: true, semanticOutput: true},
		{name: "duplicate item", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","content":[],"content":[]}}`, visible: true, semanticOutput: true},
		{name: "bad phase", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":[],"content":[]}}`, visible: true, semanticOutput: true},
		{name: "bad obfuscation", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","delta":" ","obfuscation":null}`, visible: true, semanticOutput: true},
		{name: "nonempty logprobs", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","delta":" ","logprobs":[{}]}`, visible: true, semanticOutput: true},
		{name: "bad logprobs", event: "response.output_text.done", payload: `{"type":"response.output_text.done","text":" ","logprobs":null}`, visible: true, semanticOutput: true},
		{name: "boolean sequence number", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","sequence_number":true,"delta":" ","x":true}`, visible: true, semanticOutput: true},
		{name: "boolean item index", event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"type":"reasoning","index":false,"summary":[]},"x":true}`, visible: true, semanticOutput: true},
		{name: "numeric text", event: "response.output_text.done", payload: `{"type":"response.output_text.done","text":3,"x":true}`, visible: true, semanticOutput: true},
		{name: "boolean delta", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","delta":false,"x":true}`, visible: true, semanticOutput: true},
		{name: "duplicate scalar extension", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","delta":" ","x":true,"x":false}`, visible: true, semanticOutput: true},
		{name: "fractional index", event: "response.output_text.delta", payload: `{"type":"response.output_text.delta","output_index":0.5,"delta":" "}`, visible: true, semanticOutput: true},
		{name: "refusal", event: "response.content_part.added", payload: `{"type":"response.content_part.added","part":{"type":"refusal","refusal":""}}`, visible: true, semanticOutput: true},
		{name: "unknown nested payload", event: "response.content_part.added", payload: `{"type":"response.content_part.added","part":{"type":"output_text","text":"","vendor":"x"}}`, visible: true, semanticOutput: true},
		{name: "malformed known event", event: "response.output_text.delta", payload: `{`, visible: true, semanticOutput: true},
		{name: "completed empty", event: "response.completed", payload: `{"type":"response.completed","response":{"output":[]}}`, visible: true},
		{name: "completed configuration is not output", event: "response.completed", payload: `{"type":"response.completed","response":{"id":"r","status":"completed","output":[],"text":{"format":{"type":"text"}},"metadata":null}}`, visible: true},
		{name: "completed duplicate response key", event: "response.completed", payload: `{"type":"response.completed","response":{"output":[],"output":[]}}`, visible: true, semanticOutput: true},
		{name: "completed non-array output", event: "response.completed", payload: `{"type":"response.completed","response":{"output":{}}}`, visible: true, semanticOutput: true},
		{name: "completed with text", event: "response.completed", payload: `{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}`, visible: true, semanticOutput: true},
		{name: "unknown response event metadata only", event: "response.vendor_extension", payload: `{"type":"response.vendor_extension","future":true}`, placeholder: true},
		{name: "unknown response event with recursively empty extra", event: "response.vendor_extension", payload: `{"type":"response.vendor_extension","extra":{"x":[null,{},""]}}`, placeholder: true},
		{name: "unknown response event with text remains visible", event: "response.vendor_extension", payload: `{"type":"response.vendor_extension","text":"visible"}`, visible: true, semanticOutput: true},
		{name: "known tool progress remains visible", event: "response.web_search_call.in_progress", payload: `{"type":"response.web_search_call.in_progress"}`, visible: true, semanticOutput: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResponsesEventOutputDisposition(tt.event, []byte(tt.payload))
			if !got.Matched {
				t.Fatal("event was not matched")
			}
			if got.Kind == Prelude != tt.placeholder || got.Visible != tt.visible || got.SemanticOutput != tt.semanticOutput {
				t.Fatalf("got %+v, want placeholder=%v visible=%v semantic=%v", got, tt.placeholder, tt.visible, tt.semanticOutput)
			}
		})
	}
}

// response.metadata is prelude only while the native whitelist is on. With it
// off the established metadata event keeps its legacy Unknown classification;
// this setting does not control the separate unknown-event-type rule.
func TestClassifyStreamEventResponseMetadataFollowsNativeWhitelist(t *testing.T) {
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	payload := []byte(`{"type":"response.metadata","metadata":{"openai_chatgpt_moderation_metadata":{}}}`)
	for _, tc := range []struct {
		name              string
		master, whitelist bool
		want              EventKind
	}{
		{"on", true, true, Prelude},
		{"whitelist_off", true, false, Unknown},
		{"master_off", false, true, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := Defaults()
			settings.Enabled = tc.master
			settings.Stream.NativePreludeWhitelist = tc.whitelist
			Publish(settings)
			if got := ClassifyStreamEvent("responses", "response.metadata", payload); got.Kind != tc.want {
				t.Fatalf("got %+v, want kind=%v", got, tc.want)
			}
			if got := ClassifyStreamEvent("responses", "codex.response.metadata", []byte(`{}`)); got.Kind != tc.want {
				t.Fatalf("codex.response.metadata got %+v, want kind=%v", got, tc.want)
			}
		})
	}
}

func TestClassifyStreamEventUsesConfiguredFailureRules(t *testing.T) {
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	settings := Defaults()
	settings.Failure.BalanceKeywordsExtra = []string{"wallet_marker"}
	Publish(settings)
	got := ClassifyStreamEvent("responses", "error", []byte(`{"error":{"status_code":400,"message":"wallet_marker"}}`))
	if got.Kind != TerminalFailure || !got.Retryable {
		t.Fatalf("configured account balance failure must be retryable: %+v", got)
	}
	settings.Failure.DecisionV2 = false
	Publish(settings)
	got = ClassifyStreamEvent("responses", "error", []byte(`{"error":{"status_code":400,"message":"Concurrency limit exceeded"}}`))
	if got.Retryable {
		t.Fatal("disabled decision_v2 must keep the legacy concurrency decision")
	}
	settings.Failure.DecisionV2 = true
	Publish(settings)
	got = ClassifyStreamEvent("responses", "error", []byte(`{"error":{"status_code":400,"message":"Concurrency limit exceeded"}}`))
	if !got.Retryable {
		t.Fatal("decision_v2 concurrency limit must use the configured rate-limit rule")
	}
}
