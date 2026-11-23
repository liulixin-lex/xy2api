//go:build unit

package service

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollabRegressionUnparseableLocator(t *testing.T) {
	p := ParseCollabBody([]byte(`{"input":[` + collabBatchTask + `]}`))
	for _, param := range []string{"input[-1].arguments", "input[bad].arguments", "input.foo.arguments", "input[0].arguments"} {
		t.Run(param, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "invalid_encrypted_content", "message": "Encrypted content could not be decrypted.", "param": param}})
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
			t.Logf("rescuable=%t named=%+v sends=%d task_removed=%t", o.Rescuable(), e.Named, sends, removed)
			if o.Rescuable() || sends != 0 {
				t.Error("unlocatable explicit input path must fall back without editing task")
			}
		})
	}
}
func TestCollabRegressionNestedStringFullValidation(t *testing.T) {
	for _, suffix := range []string{" trailing", ` {"error":{"code":"other"}}`, ""} {
		t.Run(suffix, func(t *testing.T) {
			inner := `{"error":{"code":"invalid_encrypted_content"}}` + suffix
			raw, _ := json.Marshal(map[string]any{"type": "proxy.upstream_error", "status": 502, "upstream_body": inner})
			e := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: raw, Event: "error", HTTPStatus: 502, Shape: resilience.CollabRequestShape{AgentCiphertext: true}})
			t.Logf("grade=%s validated=%t", e.Grade, e.Inner != nil && e.Inner.Validated)
			if suffix != "" && e.Grade != resilience.CollabEvidenceGradeUnclaimed {
				t.Error("full nested string is not a valid single JSON envelope")
			}
			if suffix == "" && e.Grade != resilience.CollabEvidenceGradeVerified {
				t.Error("valid inner control must verify")
			}
		})
	}
}
func TestCollabRegressionInheritedRepairBytes(t *testing.T) {
	raw := []byte(`{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"r","summary":[{"text":"summary"}]},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	edited, _ := p.Derive(raw, resilience.CollabActionNamedRepair, "rs_a", false)
	o := NewCollabSendObserver(p)
	b := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)), observer: o, requestBody: edited, rejection: CollabRejection{AccountID: 1, Status: 400}}
	_, _ = io.ReadAll(b)
	_ = b.Close()
	r := o.Rejections()[0]
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Evidence: p.ResolveEvidence(r.Evidence(p.Shape)), MaxAttempts: 1, Select: collabBatchSelect, Progress: progress}
	n := 0
	l.Send = func(_ context.Context, id int64, body []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		restored := gjson.GetBytes(body, "input.0.encrypted_content").Exists()
		t.Logf("account=%d action=%s handled=%v reasoning_restored=%t task_retained=%t", id, a, r.Handled, restored, gjson.GetBytes(body, "input.1.content.1.encrypted_content").Exists())
		if restored {
			t.Error("same-account derivation must retain confirmed legacy reasoning repair")
			return collabBatchEvidence("rs_a"), false
		}
		return resilience.CollabEvidence{}, true
	}
	if !l.Run(context.Background()) {
		t.Error("available named task rescue lost after reintroducing handled reasoning")
	}
	if n != 1 {
		t.Errorf("expected named task repair, got %d sends", n)
	}
}
func TestCollabRegressionAnonymousShift(t *testing.T) {
	raw := []byte(`{"input":[{"type":"compaction","encrypted_content":"summary"},{"type":"reasoning","encrypted_content":"r","summary":[{"text":"readable"}]},` + collabBatchTask + `]}`)
	input := gjson.GetBytes(raw, "input")
	repair, err := repairHTTPEncryptedItem(raw, input, input.Array()[0], 0)
	if err != nil || !repair.Changed {
		t.Fatalf("legacy compaction deletion: %+v err=%v", repair, err)
	}
	edited := repair.Body

	p := ParseCollabBody(raw)
	o := NewCollabSendObserver(p)
	b := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","param":"input[0]"}}`)), observer: o, requestBody: edited, rejection: CollabRejection{AccountID: 1, Status: 400}}
	_, _ = io.ReadAll(b)
	_ = b.Close()
	r := o.Rejections()[0]
	e := p.ResolveEvidence(r.Evidence(p.Shape))
	t.Logf("handled=%v resolved=%+v", r.Handled, e.Named)
	if !r.Handled["@input:0"] || r.Handled["@input:1"] {
		t.Error("removing anonymous compaction must not mark retained reasoning handled")
	}
	if e.Named != nil && !e.Named.Ambiguous && e.Named.Identity != "@input:1" {
		t.Error("latest physical input[0] is reasoning, not original compaction; must resolve it or fall back")
	}
}
func TestCollabRegressionNamedReplaceableBatchClass(t *testing.T) {
	// First reject a protected task, then the first replaceable ordinary message.
	raw := []byte(`{"input":[{"type":"agent_message","id":"amsg_old","recipient":"child","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"old"}]},{"type":"message","role":"assistant","content":"ack"},` + collabBatchTask + `,{"type":"reasoning","id":"rs_a","encrypted_content":"r","summary":[{"text":"readable"}]}]}`)
	p := ParseCollabBody(raw)
	progress := &CollabLadderProgress{}
	l := CollabLadder{Body: p, First: 1, Evidence: collabBatchEvidence("amsg_task"), MaxAttempts: 1, Select: collabBatchSelect, Progress: progress}
	n := 0
	l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("send=%d action=%s reasoning_retained=%t", n, a, gjson.GetBytes(b, "input.3.encrypted_content").Exists())
		if n == 1 {
			return collabBatchEvidence("amsg_old"), false
		}
		if a != resilience.CollabActionNamedRepair || !gjson.GetBytes(b, "input.3.encrypted_content").Exists() {
			t.Error("first replaceable-body rejection must replace only named body, not reasoning")
		}
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
	if n != 2 {
		t.Errorf("expected two independent named repairs, got %d", n)
	}
}

func TestCollabRegressionLosslessLocatorValidation(t *testing.T) {
	old := resilience.Current().Clone()
	defer resilience.Publish(old)
	settings := resilience.Defaults()
	settings.Collab.Ladder, settings.Collab.LosslessFirst = true, true
	resilience.Publish(settings)
	for _, param := range []string{"input[-1].arguments", "input[bad].arguments", "input.foo.arguments", "input[0].arguments", "input[0].content[1]"} {
		t.Run(param, func(t *testing.T) {
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			calls := 0
			gate := &CollabLosslessGate{Eligible: func(*Account) bool { calls++; return true }}
			ctx = WithCollabLosslessGate(ctx, gate)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			c.Set(openAIStateRejectionBodyKey, []byte(`{"input":[`+collabBatchTask+`]}`))
			payload, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "invalid_encrypted_content", "param": param}})
			err := collabPreemptLossless(ctx, c, &Account{ID: 1}, payload, 400, "")
			if param == "input[0].content[1]" {
				require.Error(t, err)
				require.Equal(t, 1, calls)
			} else {
				require.NoError(t, err)
				require.Zero(t, calls, "invalid locators must not enter optional eligibility")
				require.False(t, gate.used.Load())
			}
		})
	}
}
