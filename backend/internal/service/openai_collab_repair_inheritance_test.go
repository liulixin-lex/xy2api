//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"io"
	"strings"
	"testing"
)

func TestCollabRegressionLocatorGrammar(t *testing.T) {
	p := ParseCollabBody([]byte(`{"input":[` + collabBatchTask + `]}`))
	cases := []struct {
		name, code, message, param string
		valid                      bool
	}{
		{"valid", "invalid_encrypted_content", "Encrypted content could not be decrypted.", "input[0].content[1]", true},
		{"ownership_valid", "encrypted_agent_recovery_wire_changed", "synthetic ownership rejection", "input[0].content[1]", true},
		{"message_valid", "invalid_encrypted_content", "Encrypted content could not be decrypted at input[0].content[1].", "", true},
		{"ownership_wrong_family", "encrypted_agent_recovery_wire_changed", "Encrypted function arguments could not be decrypted.", "input[0].content[1]", false},
		{"param_trailing_text", "invalid_encrypted_content", "Encrypted content could not be decrypted.", "input[0].content[1] trailing", false},
		{"negative_control", "invalid_encrypted_content", "Encrypted content could not be decrypted.", "input[-1].arguments", false},
		{"extra_close_param", "invalid_encrypted_content", "Encrypted content could not be decrypted.", "input[0]].content[1]", false},
		{"missing_close_param", "invalid_encrypted_content", "Encrypted content could not be decrypted.", "input.0.content[1", false},
		{"bad_suffix_message", "invalid_encrypted_content", "Encrypted content could not be decrypted at input[0][bad].", "", false},
		{"negative_suffix_message", "invalid_encrypted_content", "Encrypted content could not be decrypted at input[0][-1].", "", false},
		{"fractional_index_message", "invalid_encrypted_content", "Encrypted content could not be decrypted at input[0.5].", "", false},
		{"spaced_locator", "invalid_encrypted_content", "Encrypted content could not be decrypted.", "input [bad].content", false},
		{"ownership_bad_locator", "encrypted_agent_recovery_wire_changed", "synthetic ownership rejection", "input[-1].arguments", false},
		{"ownership_wrong_field", "encrypted_agent_recovery_wire_changed", "synthetic ownership rejection", "input[0].arguments", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": tc.code, "message": tc.message, "param": tc.param}})
			r := CollabRejection{AccountID: 1, Status: 400, Payload: raw}
			o := NewCollabSendObserver(p)
			o.record(r, 0)
			e := p.ResolveEvidence(r.Evidence(p.Shape))
			sends := 0
			removed := false
			l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect, Send: func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				removed = !gjson.GetBytes(b, "input.0.content.1.encrypted_content").Exists()
				t.Logf("action=%s", a)
				return resilience.CollabEvidence{}, true
			}}
			l.Run(context.Background())
			t.Logf("rescuable=%t named=%+v sends=%d removed_task=%t", o.Rescuable(), e.Named, sends, removed)
			if !tc.valid && (o.Rescuable() || sends > 0) {
				t.Error("malformed or contradictory explicit locator must fall back")
			}
			if tc.valid && sends != 1 {
				t.Error("valid control lost its named repair")
			}
		})
	}
}

func collabObserveDerivedRejection(p CollabBody, body []byte, payload string) CollabRejection {
	o := NewCollabSendObserver(p)
	b := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(payload)), observer: o, requestBody: body, rejection: CollabRejection{AccountID: 1, Status: 400}}
	_, _ = io.ReadAll(b)
	_ = b.Close()
	return o.Rejections()[0]
}
func TestCollabRegressionInheritedIndependentRepair(t *testing.T) {
	raw := []byte(`{"top_p":0.4,"input":[` + collabBatchTask + `]}`)
	fixed, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(400, raw, []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'top_p'.","param":"top_p"}}`))
	if err != nil || !changed || gjson.GetBytes(fixed, "top_p").Exists() {
		t.Fatalf("real field repair failed: changed=%t err=%v", changed, err)
	}
	p := ParseCollabBody(raw)
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_request_error","message":"The encrypted content for item amsg_task could not be decoded."}}`)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Evidence: p.ResolveEvidence(r.Evidence(p.Shape)), MaxAttempts: 1, Select: collabBatchSelect, Progress: progress}
	n := 0
	l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		restored := gjson.GetBytes(b, "top_p").Exists()
		t.Logf("send=%d action=%s field_restored=%t inherited_derivation=%d handled=%v", n, a, restored, len(r.Derivation), r.Handled)
		if restored {
			t.Error("confirmed independent field repair restored")
			return resilience.CollabEvidence{}, false
		}
		return resilience.CollabEvidence{}, true
	}
	if !l.Run(context.Background()) {
		t.Error("available same-account rescue lost")
	}
}
func TestCollabRegressionInheritedNamedClass(t *testing.T) {
	raw := []byte(`{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"a"},{"type":"reasoning","id":"rs_b","encrypted_content":"b"},{"type":"reasoning","id":"rs_c","encrypted_content":"c"},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	edited, _ := p.Derive(raw, resilience.CollabActionNamedRepair, "rs_a", false)
	r := collabObserveDerivedRejection(p, edited, `{"error":{"code":"invalid_encrypted_content","item_id":"rs_b"}}`)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Evidence: p.ResolveEvidence(r.Evidence(p.Shape)), MaxAttempts: 4, Select: collabBatchSelect, Progress: progress}
	n := 0
	l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("send=%d action=%s rs_c_retained=%t handled=%v", n, a, gjson.GetBytes(b, "input.2.encrypted_content").Exists(), r.Handled)
		if gjson.GetBytes(b, "input.2.encrypted_content").Exists() {
			t.Error("second named reasoning rejection must use batch allowance")
			return collabBatchEvidence("rs_c"), false
		}
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
	if n != 1 {
		t.Errorf("wanted one batch send after inherited first named repair, got %d", n)
	}
}
func TestCollabRegressionReplaceableClasses(t *testing.T) {
	for _, kind := range []string{"tool_tool", "tool_agent", "agent_tool"} {
		t.Run(kind, func(t *testing.T) {
			tool := func(id string) string {
				return fmt.Sprintf(`{"type":"function_call_output","id":"%s","call_id":"call_fixture","output":[{"type":"encrypted_content","encrypted_content":"tool"}]}`, id)
			}
			agent := func(id string) string {
				return fmt.Sprintf(`{"type":"agent_message","id":"%s","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"old"}]}`, id)
			}
			a, b := tool("fco_a"), tool("fco_b")
			ia, ib := "fco_a", "fco_b"
			if kind == "tool_agent" {
				b, ib = agent("amsg_b"), "amsg_b"
			}
			if kind == "agent_tool" {
				a, ia = agent("amsg_a"), "amsg_a"
			}
			p := ParseCollabBody([]byte(`{"input":[` + a + `,` + b + `,{"type":"reasoning","id":"rs_a","encrypted_content":"r"},` + collabBatchTask + `]}`))
			l := CollabLadder{Body: p, First: 1, Evidence: collabBatchEvidence(ia), MaxAttempts: 4, Select: collabBatchSelect}
			n := 0
			l.Send = func(_ context.Context, _ int64, body []byte, act resilience.CollabAction) (resilience.CollabEvidence, bool) {
				n++
				t.Logf("send=%d action=%s classes=%s/%s reasoning=%t", n, act, p.Items[0].Class, p.Items[1].Class, gjson.GetBytes(body, "input.2.encrypted_content").Exists())
				if n == 1 {
					return collabBatchEvidence(ib), false
				}
				if act != resilience.CollabActionDropUnprotected {
					t.Error("second replaceable item must use drop-unprotected class allowance")
				}
				return resilience.CollabEvidence{}, true
			}
			l.Run(context.Background())
			if n != 2 {
				t.Errorf("wanted first-item then batch sends, got %d", n)
			}
		})
	}
}
