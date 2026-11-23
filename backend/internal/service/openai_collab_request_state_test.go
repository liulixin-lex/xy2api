//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"testing"
)

func TestCollabRegressionQuotedIdentity(t *testing.T) {
	p := ParseCollabBody([]byte(`{"input":[` + collabBatchTask + `]}`))
	for _, suffix := range []string{".missing", "/missing", ":missing", "", "_missing"} {
		t.Run(suffix, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "invalid_request_error", "message": "Encrypted content hydration failed for item 'amsg_task" + suffix + "'."}})
			r := CollabRejection{AccountID: 1, Status: 400, Payload: raw}
			o := NewCollabSendObserver(p)
			o.record(r, 0)
			e := p.ResolveEvidence(r.Evidence(p.Shape))
			sends, hooks := 0, 0
			l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect, BeforeDisposal: func(context.Context, resilience.CollabEvidence) bool { hooks++; return false }, Send: func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				t.Logf("action=%s task_kept=%t", a, gjson.GetBytes(b, "input.0.content.1.encrypted_content").Exists())
				return resilience.CollabEvidence{}, true
			}}
			l.Run(context.Background())
			t.Logf("suffix=%q named=%+v rescuable=%t sends=%d hooks=%d", suffix, e.Named, o.Rescuable(), sends, hooks)
			if suffix != "" && (o.Rescuable() || sends != 0 || hooks != 0) {
				t.Error("nonexistent quoted full identity resolved by prefix")
			}
			if suffix == "" && sends != 1 {
				t.Error("valid identity control failed")
			}
		})
	}
}

func TestCollabRegressionOwnershipNamed(t *testing.T) {
	for _, tc := range []struct{ name, code, id, body string }{
		{"unknown_named", "encrypted_agent_recovery_new_state", "amsg_task", `{"input":[` + collabBatchTask + `]}`},
		{"unknown_unnamed_control", "encrypted_agent_recovery_new_state", "", `{"input":[` + collabBatchTask + `]}`},
		{"recognized_without_agent", "encrypted_agent_recovery_wire_changed", "rs_a", `{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"r","summary":[{"text":"keep"}]}]}`},
	} {
		for _, attempts := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, attempts), func(t *testing.T) {
				p := ParseCollabBody([]byte(tc.body))
				raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": tc.code, "message": "synthetic ownership rejection", "item_id": tc.id}})
				e := p.ResolveEvidence((CollabRejection{Payload: raw, Status: 409}).Evidence(p.Shape))
				sends, edits, hooks := 0, 0, 0
				l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: attempts, Select: collabBatchSelect, BeforeDisposal: func(context.Context, resilience.CollabEvidence) bool { hooks++; return false }}
				l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
					sends++
					changed := !bytes.Equal(b, p.Original)
					if changed {
						edits++
					}
					t.Logf("account=%d action=%s content_changed=%t", id, a, changed)
					return e, changed
				}
				l.Run(context.Background())
				t.Logf("sends=%d edits=%d hooks=%d", sends, edits, hooks)
				if edits != 0 || hooks != 0 {
					t.Error("ownership branch permitting only original switch authorized disposal")
				}
			})
		}
	}
}

func TestCollabRegressionIndependentRepairOnNewAccount(t *testing.T) {
	raw := []byte(`{"top_p":0.4,"input":[` + collabBatchTask + `]}`)
	fixed, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(400, raw, []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'top_p'.","param":"top_p"}}`))
	if err != nil || !changed {
		t.Fatal("repair failed", err)
	}
	p := ParseCollabBody(raw)
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_request_error","message":"The encrypted content for item amsg_task could not be decoded."}}`)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Evidence: p.ResolveEvidence(r.Evidence(p.Shape)), MaxAttempts: 4, Select: collabBatchSelect, Progress: progress}
	l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		restored := gjson.GetBytes(b, "top_p").Exists()
		t.Logf("account=%d action=%s field_restored=%t", id, a, restored)
		if restored {
			t.Error("independent field repair not inherited by target")
		}
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
}

func TestCollabRegressionNamedClassAcrossAccounts(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	n := 0
	l := CollabLadder{Body: p, First: 1, Evidence: collabBatchEvidence("rs_a"), MaxAttempts: 4, Select: collabBatchSelect}
	l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		n++
		t.Logf("send=%d account=%d action=%s rs_a_kept=%t rs_b_kept=%t", n, id, a, gjson.GetBytes(b, "input.0.encrypted_content").Exists(), gjson.GetBytes(b, "input.1.encrypted_content").Exists())
		if n == 1 {
			return collabBatchEvidence("amsg_task"), false
		}
		if n == 2 {
			return collabBatchEvidence("rs_b"), false
		}
		if gjson.GetBytes(b, "input.0.encrypted_content").Exists() {
			t.Error("second named reasoning item renewed class allowance on new account")
		}
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
}
