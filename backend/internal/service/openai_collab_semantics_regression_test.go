//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const collabBatchTask = `{"type":"agent_message","id":"amsg_task","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"task"}]}`
const collabBatchBody = `{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"r1","summary":[{"text":"summary1"}]},{"type":"reasoning","id":"rs_b","encrypted_content":"r2","summary":[{"text":"summary2"}]},` + collabBatchTask + `]}`

func collabBatchEvidence(id string) resilience.CollabEvidence {
	e := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	if id != "" {
		e.Named = &resilience.CollabNamedItem{Identity: id}
	}
	return e
}
func collabBatchSelect(_ context.Context, preferred int64, excluded map[int64]bool) (int64, func()) {
	if preferred > 0 && !excluded[preferred] {
		return preferred, func() {}
	}
	for _, a := range []int64{1, 2} {
		if !excluded[a] {
			return a, func() {}
		}
	}
	return 0, nil
}
func TestCollabRegressionNamedBatchAfterThreeOptional(t *testing.T) {
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBatchBody)), First: 1, Evidence: collabBatchEvidence(""), MaxAttempts: 4, Select: collabBatchSelect}
	n := 0
	l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("send=%d account=%d action=%s task=%t", n, id, a, gjson.GetBytes(b, "input.2.content.1.encrypted_content").Exists())
		switch n {
		case 1, 2:
			return collabBatchEvidence(""), false
		case 3:
			return collabBatchEvidence("rs_a"), false
		case 4:
			return collabBatchEvidence("rs_b"), false
		}
		if id != 2 || a != resilience.CollabActionNamedRepair || gjson.GetBytes(b, "input.0.encrypted_content").Exists() || gjson.GetBytes(b, "input.1.encrypted_content").Exists() || !gjson.GetBytes(b, "input.2.content.1.encrypted_content").Exists() {
			t.Error("named reasoning batch spent optional quota and replaced protected task on first account")
		}
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
	if n != 5 {
		t.Errorf("wanted 5 sends, got %d", n)
	}
}
func TestCollabRegressionNamedBatchHandledAgain(t *testing.T) {
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBatchBody)), First: 1, Evidence: collabBatchEvidence("rs_a"), MaxAttempts: 4, Select: collabBatchSelect}
	n := 0
	l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("send=%d account=%d action=%s rs_b=%t", n, id, a, gjson.GetBytes(b, "input.1.encrypted_content").Exists())
		if n >= 3 {
			return resilience.CollabEvidence{}, true
		}
		return collabBatchEvidence("rs_b"), false
	}
	l.Run(context.Background())
	if n != 2 {
		t.Errorf("same batch-handled reasoning named again must stop; got %d sends", n)
	}
}
func TestCollabRegressionWrongFamilyLocators(t *testing.T) {
	p := ParseCollabBody([]byte(`{"input":[` + collabBatchTask + `,{"type":"function_call","id":"fc_a","call_id":"call_a","name":"dispatch","arguments":"cipher","encrypted_function_args":["message"]}]}`))
	for _, tc := range []struct {
		name, message, param, id string
		valid                    bool
	}{
		{"param_wrong_field", "Encrypted function arguments could not be decrypted.", "input[0].arguments", "", false},
		{"message_wrong_field", "Encrypted function arguments at input[0].arguments could not be decrypted.", "", "", false},
		{"identity_wrong_family", "Encrypted function arguments for item amsg_task could not be decrypted.", "", "", false},
		{"index_wrong_family", "Encrypted function arguments could not be decrypted.", "input[0]", "", false},
		{"explicit_id_wrong_family", "Encrypted function arguments could not be decrypted.", "", "amsg_task", false},
		{"valid_arguments", "Encrypted function arguments could not be decrypted.", "input[1].arguments", "", true},
		{"message_path_wrong_field", "Encrypted content at input[0].arguments could not be decrypted.", "", "", false},
		{"conflicting_fields", "Encrypted content at input[0].arguments could not be decrypted.", "input[0].content[1]", "", false},
		{"malformed_param", "Encrypted content could not be decrypted.", "input[0].content[1] trailing", "", false},
		{"consistent_fields", "Encrypted content at input[0].content[1] could not be decrypted.", "input[0].content[1]", "amsg_task", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "invalid_encrypted_content", "message": tc.message, "param": tc.param, "item_id": tc.id}})
			o := NewCollabSendObserver(p)
			r := CollabRejection{AccountID: 1, Status: 400, Payload: raw}
			o.record(r, 0)
			e := p.ResolveEvidence(r.Evidence(p.Shape))
			t.Logf("rescuable=%t evidence=%+v named=%+v", o.Rescuable(), e, e.Named)
			if o.Rescuable() != tc.valid {
				t.Errorf("wrong locator accepted=%t want=%t", o.Rescuable(), tc.valid)
			}
			if !tc.valid && o.Rescuable() {
				l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect}
				l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
					t.Logf("incorrect action=%s task_removed=%t", a, !gjson.GetBytes(b, "input.0.content.1.encrypted_content").Exists())
					return resilience.CollabEvidence{}, true
				}
				l.Run(context.Background())
			}
		})
	}
}
func TestCollabRegressionActiveConsumedTask(t *testing.T) {
	raw := []byte(`{"input":[` + collabBatchTask + `,{"type":"message","role":"assistant","phase":"commentary","content":"working"}]}`)
	for _, consumed := range []bool{false, true} {
		p := ParseCollabBodyWithContext(raw, CollabBodyContext{Thread: "child", Consumed: map[string]bool{"amsg_task": consumed}})
		t.Logf("consumed=%t class=%s", consumed, p.Items[0].Class)
		if p.Items[0].Class != resilience.CollabItemClassProtected {
			t.Error("consumption does not finish an ongoing task")
		}
	}
	p := ParseCollabBodyWithContext(raw, CollabBodyContext{Thread: "another"})
	t.Logf("inherited foreign task class=%s", p.Items[0].Class)
	if p.Items[0].Class != resilience.CollabItemClassReplaceable {
		t.Error("historical task for another thread must be replaceable")
	}
}
func TestCollabRegressionAnonymousHandled(t *testing.T) {
	raw := []byte(`{"input":[{"type":"reasoning","encrypted_content":"r","summary":[{"text":"summary"}]},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	edited, _ := p.Derive(raw, resilience.CollabActionNamedRepair, "@input:0", false)
	o := NewCollabSendObserver(p)
	b := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","param":"input[0]"}}`)), observer: o, requestBody: edited, rejection: CollabRejection{AccountID: 1, Status: 400, Source: resilience.CollabEvidenceSourceHttpError}}
	_, _ = io.ReadAll(b)
	_ = b.Close()
	r := o.Rejections()[0]
	t.Logf("handled=%v", r.Handled)
	if !r.Handled["@input:0"] {
		t.Error("legacy anonymous reasoning repair was not inherited")
	}
	progress := &CollabLadderProgress{}
	progress.RememberHandled(1, r.Handled)
	l := CollabLadder{Body: p, First: 1, Evidence: p.ResolveEvidence(r.Evidence(p.Shape)), MaxAttempts: 2, NamedReasoningAllowed: func(int64) bool { return false }, Progress: progress, Select: collabBatchSelect}
	n := 0
	l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("extra action=%s task_removed=%t", a, !gjson.GetBytes(b, "input.1.content.1.encrypted_content").Exists())
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
	if n != 0 {
		t.Errorf("already handled reasoning permits no new send, got %d", n)
	}
}
func TestCollabRegressionEvidenceGrammar(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		want      resilience.CollabEvidenceGrade
	}{
		{"plain", "The encrypted content for item rs_a could not be verified.", resilience.CollabEvidenceGradeVerified},
		{"quoted_item", "The encrypted content for item \"rs_a\" could not be verified.", resilience.CollabEvidenceGradeVerified},
		{"single_quoted_example", "The encrypted content schema field must contain the literal 'failed to decrypt encrypted content'; got a boolean.", resilience.CollabEvidenceGradeUnclaimed},
		{"backtick_example", "Encrypted content validation requires the example `could not be decrypted` in the schema description.", resilience.CollabEvidenceGradeUnclaimed},
		{"negated", "The encrypted content was accepted; this is not a failed to decrypt error. The tool schema is invalid.", resilience.CollabEvidenceGradeUnclaimed},
		{"unrelated_failure_suffix", "The encrypted content is valid. Failed to decrypt a TLS certificate private key.", resilience.CollabEvidenceGradeUnclaimed},
	} {
		for _, source := range []resilience.CollabEvidenceSource{resilience.CollabEvidenceSourceHttpError, resilience.CollabEvidenceSourceFailedJson, resilience.CollabEvidenceSourceStreamError, resilience.CollabEvidenceSourceAggregatedStream} {
			t.Run(tc.name+"/"+string(source), func(t *testing.T) {
				raw, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "invalid_request_error", "message": tc.msg}})
				e := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: raw, Source: source, HTTPStatus: 400, Shape: resilience.CollabRequestShape{AgentCiphertext: true}})
				t.Logf("grade=%s named=%+v", e.Grade, e.Named)
				if e.Grade != tc.want {
					t.Errorf("got %s want %s", e.Grade, tc.want)
				}
			})
		}
	}
}
func TestCollabRegressionNestedEnvelope(t *testing.T) {
	inner := `{"error":{"code":"invalid_encrypted_content"}}`
	for _, depth := range []int{1, 2} {
		raw := inner
		for i := 0; i < depth; i++ {
			b, _ := json.Marshal(map[string]any{"type": "proxy.upstream_error", "status": 400, "error": map[string]string{"code": "gateway", "message": "wrapped"}, "upstream_body": json.RawMessage(raw)})
			raw = string(b)
		}
		e := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: []byte(raw), Event: "error", Source: resilience.CollabEvidenceSourceStreamError, HTTPStatus: 200, Shape: resilience.CollabRequestShape{AgentCiphertext: true}})
		if e.Grade != resilience.CollabEvidenceGradeVerified || e.Inner == nil || !e.Inner.Validated {
			t.Fatalf("depth=%d evidence=%+v", depth, e)
		}
	}

}
func TestCollabRegressionNestedValidation(t *testing.T) {
	for _, inner := range []string{
		`{"type":"proxy.upstream_error","status":"invalid","error":{"code":"invalid_encrypted_content"}}`,
		`{"type":"response.failed","response":{"error":{"type":"upstream_error","status_code":"400","code":"401","message":"Encrypted content could not be decrypted"}}}`,
	} {
		raw, _ := json.Marshal(map[string]any{"type": "proxy.upstream_error", "status": 502, "upstream_body": json.RawMessage(inner)})
		e := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: raw, Event: "error", Source: resilience.CollabEvidenceSourceStreamError, HTTPStatus: 200, Shape: resilience.CollabRequestShape{AgentCiphertext: true}})
		t.Logf("grade=%s validated=%t", e.Grade, e.Inner != nil && e.Inner.Validated)
		if e.Grade != resilience.CollabEvidenceGradeUnclaimed {
			t.Error("malformed inner wrapper accepted through valid outer wrapper")
		}
	}
}
func TestCollabRegressionHandledProtectedToolOutput(t *testing.T) {
	raw := []byte(`{"input":[{"type":"function_call_output","id":"fco_a","call_id":"call_a","output":[{"type":"encrypted_content","encrypted_content":"tool"}]},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	progress := &CollabLadderProgress{}
	progress.RememberHandled(1, map[string]bool{"fco_a": true})
	n := 0
	l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: collabBatchEvidence("fco_a"), MaxAttempts: 4, Progress: progress, Select: collabBatchSelect}
	l.Send = func(_ context.Context, id int64, _ []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("action=%s account=%d", a, id)
		if a != resilience.CollabActionReplaceAll || id != 1 {
			t.Error("protected endpoint must be one local replace-all")
		}
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
	t.Logf("class=%s family=%s sends=%d", p.Items[0].Class, p.Items[0].Family, n)
	if n != 1 {
		t.Error("protected tool output lost its independent final-disposal allowance")
	}
}
