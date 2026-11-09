package resilience

import (
	"strings"
	"testing"
)

func TestResponsesUnknownReadyDisposition(t *testing.T) {
	for _, tc := range []struct {
		name, event, payload string
		prelude              bool
	}{
		{"gateway ready", "sub2api.gateway_ready", `{"type":"sub2api.gateway_ready"}`, true},
		{"other namespace", "relay.ready", `{"type":"relay.ready","ready":true,"elapsed":0,"metadata":{"empty":[null,{},""]}}`, true},
		{"future response event", "response.vendor_ready", `{"type":"response.vendor_ready","id":"opaque","sequence_number":2}`, true},
		{"SSE type only", "relay.ready", `{}`, true},
		{"payload type only", "", `{"type":"relay.ready"}`, true},
		{"source and model output", "sub2api.gateway_ready", `{"type":"sub2api.gateway_ready","source":"relay","model_output":false}`, true},
		{"text remains staged", "relay.ready", `{"type":"relay.ready","text":"hello"}`, true},
		{"nested content remains staged", "relay.ready", `{"type":"relay.ready","extra":{"x":[{"text":"hello"}]}}`, true},
		{"whitespace remains staged", "relay.ready", `{"type":"relay.ready","text":" "}`, true},
		{"encrypted content remains staged", "relay.ready", `{"type":"relay.ready","encrypted_content":"opaque"}`, true},
		{"tool identity remains staged", "relay.ready", `{"type":"relay.ready","item":{"type":"function_call","name":"tool"}}`, true},
		{"malformed remains visible", "relay.ready", `{"type":"relay.ready"`, false},
		{"not object remains visible", "relay.ready", `[]`, false},
		{"duplicate remains visible", "relay.ready", `{"type":"relay.ready","ready":true,"ready":false}`, false},
		{"wrong identity type remains visible", "relay.ready", `{"type":"relay.ready","id":true}`, false},
		{"wrong event type remains visible", "relay.ready", `{"type":true}`, false},
		{"conflicting types remains visible", "relay.ready", `{"type":"other.ready"}`, false},
		{"known declaration", "response.output_text.delta", `{"type":"relay.ready"}`, false},
		{"untyped object", "", `{}`, false},
		{"bounded recursion", "relay.ready", `{"type":"relay.ready","extra":` + strings.Repeat(`[`, responsesEmptyFieldMaxDepth+1) + `null` + strings.Repeat(`]`, responsesEmptyFieldMaxDepth+1) + `}`, false},
		{"known tool progress", "response.web_search_call.in_progress", `{"type":"response.web_search_call.in_progress"}`, false},
		{"known refusal", "response.refusal.done", `{"type":"response.refusal.done","refusal":""}`, false},
		{"known malformed delta", "response.output_text.delta", `{"type":"response.output_text.delta"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResponsesEventOutputDisposition(tc.event, []byte(tc.payload))
			if !got.Matched || (got.Kind == Prelude) != tc.prelude || got.Visible == tc.prelude || got.SemanticOutput == tc.prelude {
				t.Fatalf("got %+v, want prelude=%t", got, tc.prelude)
			}
			if class := ClassifyStreamEvent("responses", tc.event, []byte(tc.payload)); (class.Kind == Prelude) != tc.prelude {
				t.Fatalf("classification %+v disagrees with disposition %+v", class, got)
			}
		})
	}
}

func TestResponsesKnownEventSetCoversOfficialAndGatewayTypes(t *testing.T) {
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	Publish(Defaults())
	for _, eventType := range []string{
		"response.audio.transcript.delta", "response.audio.transcript.done",
		"response.code_interpreter_call_code.delta", "response.code_interpreter_call_code.done",
		"response.compaction.compacting", "response.shell_call_command.added",
		"response.shell_call_command.delta", "response.shell_call_command.done",
		"response.shell_call_output_content.delta", "response.shell_call_output_content.done",
		"response.output_text.annotation.added", "response.failed", "error", "keepalive", "ping",
		"codex.response.metadata", "codex.rate_limits",
	} {
		if !isKnownResponsesEventType(eventType) {
			t.Errorf("known protocol event type %q is missing from the shared set", eventType)
		}
	}

	for _, tc := range []struct {
		event   string
		payload string
		kind    EventKind
	}{
		{"response.created", `{"type":"response.created","source":"synthetic"}`, Prelude},
		{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"synthetic"}`, Content},
		{"response.output_text.delta", `{"type":"response.output_text.delta","delta":" "}`, Prelude},
		{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"","extra":"synthetic"}`, Content},
		{"response.output_item.added", `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"synthetic-cipher"}}`, Content},
		{"response.web_search_call.in_progress", `{"type":"response.web_search_call.in_progress"}`, Content},
		{"response.audio.transcript.delta", `{"type":"response.audio.transcript.delta"}`, Content},
		{"response.completed", `{"type":"response.completed","response":{"output":[]}}`, TerminalSuccess},
		{"response.incomplete", `{"type":"response.incomplete"}`, TerminalFailure},
		{"keepalive", `{"type":"keepalive"}`, Prelude},
		{"codex.rate_limits", `{"type":"codex.rate_limits"}`, Prelude},
		{"relay.ready", `{"type":"response.created"}`, Prelude},
		{"response.output_text.delta", `{"type":"response.created"}`, Content},
		{"response.metadata", `{"type":"codex.response.metadata"}`, Prelude},
		{"error", `{"type":"error"}`, TerminalFailure},
		{"response.failed", `{"type":"response.failed"}`, TerminalFailure},
		{"ping", `{"type":"ping"}`, Content},
	} {
		if got := ClassifyStreamEvent("responses", tc.event, []byte(tc.payload)).Kind; got != tc.kind {
			t.Errorf("%s classified as %v, want %v", tc.event, got, tc.kind)
		}
	}
}
