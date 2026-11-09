package resilience

import (
	"strings"
	"testing"
)

func TestG2BB2UnknownMetadataBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, event, payload string
		prelude              bool
	}{
		// Only the recorded keys/types are reused; all values are synthetic.
		{"355 shape", "sub2api.gateway_ready", `{"type":"sub2api.gateway_ready","source":"synthetic-gateway","model_output":false}`, true},
		{"external nested strings", "relay.ready", `{"type":"relay.ready","extra":{"items":[{"text":"synthetic"}],"space":" "}}`, true},
		{"external SSE type only", "relay.ready", `{"source":"synthetic"}`, true},
		{"external payload type only", "", `{"type":"relay.ready","source":"synthetic"}`, true},
		{"prefix is case sensitive", "Response.ready", `{"type":"Response.ready","source":"synthetic"}`, true},
		{"prefix requires dot", "codex_ready", `{"type":"codex_ready","source":"synthetic"}`, true},
		{"strict response empty", "response.future", `{"type":"response.future","extra":{"values":[null,0,false,""]}}`, true},
		{"strict codex empty", "codex.future", `{"type":"codex.future","extra":{"values":[null,0,false,""]}}`, true},
		{"strict identity metadata", "response.future", `{"type":"response.future","id":"synthetic-id","response_id":"synthetic-response","item_id":"synthetic-item","sequence_number":2}`, true},
		{"response source", "response.future", `{"type":"response.future","source":"synthetic"}`, false},
		{"codex source", "codex.future", `{"type":"codex.future","source":"synthetic"}`, false},
		{"response nested text", "response.future", `{"type":"response.future","extra":[{"text":"synthetic"}]}`, false},
		{"codex whitespace", "codex.future", `{"type":"codex.future","text":" "}`, false},
		{"response ciphertext", "response.future", `{"type":"response.future","encrypted_content":"synthetic-cipher"}`, false},
		{"codex tool identity", "codex.future", `{"type":"codex.future","item":{"type":"function_call","name":"synthetic-tool"}}`, false},
		{"external nested duplicate", "relay.ready", `{"type":"relay.ready","extra":{"x":true,"x":false}}`, false},
		{"external array duplicate", "relay.ready", `{"type":"relay.ready","extra":[{"x":true,"x":false}]}`, false},
		{"escaped duplicate", "relay.ready", `{"type":"relay.ready","extra":{"x":true,"\u0078":false}}`, false},
		{"strict nested duplicate", "response.future", `{"type":"response.future","extra":{"x":true,"x":false}}`, false},
		{"strict array duplicate", "codex.future", `{"type":"codex.future","extra":[{"x":true,"x":false}]}`, false},
		{"external root duplicate", "relay.ready", `{"type":"relay.ready","source":"a","source":"b"}`, false},
		{"external malformed", "relay.ready", `{"type":"relay.ready"`, false},
		{"external nonobject", "relay.ready", `[]`, false},
		{"external wrong type", "relay.ready", `{"type":true}`, false},
		{"external wrong id", "relay.ready", `{"type":"relay.ready","id":true}`, false},
		{"external fractional sequence", "relay.ready", `{"type":"relay.ready","sequence_number":0.5}`, false},
		{"external negative sequence", "relay.ready", `{"type":"relay.ready","sequence_number":-1}`, false},
		{"unknown type mismatch", "relay.ready", `{"type":"other.ready"}`, false},
		{"response declaration mismatch", "response.future", `{"type":"relay.ready"}`, false},
		{"codex declaration mismatch", "codex.future", `{"type":"relay.ready"}`, false},
		{"depth limit", "relay.ready", `{"type":"relay.ready","extra":` + strings.Repeat(`[`, 32) + `"synthetic"` + strings.Repeat(`]`, 32) + `}`, true},
		{"depth overflow", "relay.ready", `{"type":"relay.ready","extra":` + strings.Repeat(`[`, 33) + `null` + strings.Repeat(`]`, 33) + `}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResponsesEventOutputDisposition(tc.event, []byte(tc.payload))
			t.Logf("kind=%d visible=%t semantic=%t", got.Kind, got.Visible, got.SemanticOutput)
			if !got.Matched || (got.Kind == Prelude) != tc.prelude || got.Visible == tc.prelude || got.SemanticOutput == tc.prelude || got.BlankPlaceholder {
				t.Fatalf("got %+v, want prelude=%t without blank-placeholder marker", got, tc.prelude)
			}
			if class := ClassifyStreamEvent("responses", tc.event, []byte(tc.payload)); (class.Kind == Prelude) != tc.prelude {
				t.Fatalf("classification %+v disagrees with disposition %+v", class, got)
			}
		})
	}
}
