//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"testing"
)

func TestCollabDisposalContradictoryLocators(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	for _, second := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(second), func(t *testing.T) {
			fields := map[string]string{"code": "invalid_encrypted_content", "message": "Encrypted content could not be decrypted at input[0].", "param": fmt.Sprintf("input[%d]", second)}
			raw, _ := json.Marshal(map[string]any{"error": fields})
			e := p.ResolveEvidence((CollabRejection{Payload: raw, Status: 400}).Evidence(p.Shape))
			t.Logf("resolved=%+v", e.Named)
			require.NotNil(t, e.Named)
			require.Equal(t, second == 0, e.Named.Located, "all separately supplied locators must agree")
		})
	}
}

func TestCollabDisposalStructuredIDType(t *testing.T) {
	for _, id := range []string{"123", "true"} {
		t.Run(id, func(t *testing.T) {
			raw := []byte(`{"input":[{"type":"reasoning","id":"` + id + `","encrypted_content":"r"}]}`)
			p := ParseCollabBody(raw)
			e := p.ResolveEvidence((CollabRejection{Payload: []byte(`{"error":{"code":"invalid_encrypted_content","item_id":` + id + `}}`)}).Evidence(p.Shape))
			sends := 0
			l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				return resilience.CollabEvidence{}, true
			}}
			l.Run(context.Background())
			t.Logf("nonstring_id=%s resolved=%+v sends=%d", id, e.Named, sends)
			require.Zero(t, sends, "a non-string ID is not byte-exact string reference membership")
		})
	}
}

func TestCollabDisposalDeadlineEntry(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	for _, grade := range []resilience.CollabEvidenceGrade{resilience.CollabEvidenceGradeVerified, resilience.CollabEvidenceGradeClaimed, resilience.CollabEvidenceGradeUnclaimed} {
		t.Run(string(grade), func(t *testing.T) {
			e := collabBatchEvidence("")
			e.Grade = grade
			sends := 0
			l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: e, MaxAttempts: 4, Select: collabBatchSelect, Continue: func() bool { return false }, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				return e, false
			}}
			require.False(t, l.Run(context.Background()))
			require.Zero(t, sends)
			t.Logf("grade=%s stop=%v local=%t", grade, l.StopReason, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
			require.Equal(t, grade == resilience.CollabEvidenceGradeVerified, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
		})
	}
}

func TestCollabDisposalAlreadyReplacedDoesNotRestart(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	final, changed := p.Derive(p.Original, resilience.CollabActionReplaceAll, "", false)
	require.True(t, changed)
	for _, attempts := range []int{1, 4, 0} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			progress := &CollabLadderProgress{derived: map[int64][]byte{1: final}, probed: map[int64]bool{}, replaced: map[int64]bool{1: true}, handled: map[int64]map[string]bool{1: {"rs_a": true, "rs_b": true, "amsg_task": true}}}
			sends, hooks := 0, 0
			e := collabBatchEvidence("")
			l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: e, MaxAttempts: attempts, Progress: progress, Select: collabBatchSelect}
			l.BeforeDisposal = func(context.Context, resilience.CollabEvidence) bool { hooks++; return true }
			l.Send = func(_ context.Context, account int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				t.Logf("send account=%d action=%s task_cipher=%t", account, a, gjson.GetBytes(b, "input.2.content.1.encrypted_content").Exists())
				return e, true
			}
			delivered := l.Run(context.Background())
			if !delivered && p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding {
				delivered = l.BeforeDisposal(context.Background(), l.LastEvidence)
			}
			t.Logf("attempts=%d sends=%d local_hooks=%d stop=%v delivered=%t", attempts, sends, hooks, l.StopReason, delivered)
			require.Zero(t, sends, "replace-all already rejected: row (b) ends locally or falls back, never restarts selection")
			require.Equal(t, 1, hooks)
		})
	}
}

func collabDisposalNestedRepair(t *testing.T, identified bool) {
	id := ""
	if identified {
		id = `"id":"msg_plain",`
	}
	raw := []byte(`{"input":[{"type":"message",` + id + `"role":"user","content":[{"type":"input_text","text":"plain","bad":true}]},` + collabBatchTask + `]}`)
	for _, code := range []string{"invalid_encrypted_content", "invalid_request_error", "server_error", "rate_limit_exceeded", "context_length_exceeded"} {
		t.Run(code, func(t *testing.T) {
			p := ParseCollabBody(raw)
			fixed, err := sjson.DeleteBytes(raw, "input.0.content.0.bad")
			require.NoError(t, err)
			rejection := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"`+code+`"}}`)
			rejection.AccountID = 1
			progress := &CollabLadderProgress{}
			progress.RememberRejection(rejection)
			t.Logf("identified=%t unlocatable=%t repairs=%v", identified, rejection.unlocatable, progress.repairs)
			for _, account := range []int64{2, 3} {
				t.Run(fmt.Sprint(account), func(t *testing.T) {
					sent := false
					l := CollabLadder{Body: p, First: 1, Evidence: collabBatchEvidence("amsg_task"), MaxAttempts: 1, Progress: progress, Select: func(context.Context, int64, map[int64]bool) (int64, func()) { return account, func() {} }}
					l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
						sent = true
						t.Logf("account=%d action=%s bad_restored=%t", id, a, gjson.GetBytes(b, "input.0.content.0.bad").Exists())
						require.False(t, gjson.GetBytes(b, "input.0.content.0.bad").Exists(), "confirmed nested repair must survive account switch")
						return resilience.CollabEvidence{}, true
					}
					l.Run(context.Background())
					require.True(t, sent)
				})
			}
		})
	}
}
func TestCollabDisposalAnonymousNestedRepair(t *testing.T)  { collabDisposalNestedRepair(t, false) }
func TestCollabDisposalIdentifiedNestedRepair(t *testing.T) { collabDisposalNestedRepair(t, true) }

func TestCollabDisposalExpiredHandledEndpoints(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	for _, id := range []string{"rs_a", "amsg_task"} {
		t.Run(id, func(t *testing.T) {
			e := collabBatchEvidence(id)
			progress := &CollabLadderProgress{handled: map[int64]map[string]bool{1: {id: true}}}
			l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: e, MaxAttempts: 4, Progress: progress, Continue: func() bool { return false }, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
				t.Fatal("expired budget must not send")
				return e, false
			}}
			l.Run(context.Background())
			allowed := p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding
			t.Logf("id=%s stop=%v local_ending=%t", id, l.StopReason, allowed)
			require.Equal(t, id == "amsg_task", allowed, "handled nonprotected endpoint remains fallback even if time expires")
		})
	}
}

func TestCollabDisposalRealNestedFieldRepair(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"plain","prompt_cache_breakpoint":true}]},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(400, raw, []byte(`{"error":{"code":"invalid_parameter","message":"prompt_cache_breakpoint is not supported on this model","param":"prompt_cache_breakpoint"}}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(fixed, "input.0.content.0.prompt_cache_breakpoint").Exists())
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"server_error"}}`)
	r.AccountID = 1
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	sent := false
	l := CollabLadder{Body: p, First: 1, Evidence: collabBatchEvidence("amsg_task"), MaxAttempts: 4, Progress: progress, Select: collabBatchSelect}
	l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		sent = true
		t.Logf("legacy_reason=%s account=%d action=%s restored=%t unlocatable=%t repairs=%v", reason, id, a, gjson.GetBytes(b, "input.0.content.0.prompt_cache_breakpoint").Exists(), r.unlocatable, progress.repairs)
		require.False(t, gjson.GetBytes(b, "input.0.content.0.prompt_cache_breakpoint").Exists())
		return resilience.CollabEvidence{}, true
	}
	l.Run(context.Background())
	require.True(t, sent)
}

func TestCollabDisposalReplaceAllTransition(t *testing.T) {
	for _, attempts := range []int{1, 4, 0} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			p := ParseCollabBody([]byte(collabBatchBody))
			repaired, changed := p.Derive(p.Original, resilience.CollabActionNamedRepair, "amsg_task", false)
			require.True(t, changed)
			r := collabObserveDerivedRejection(p, repaired, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
			r.AccountID = 1
			progress := &CollabLadderProgress{}
			progress.RememberRejection(r)
			require.True(t, progress.handled[1]["amsg_task"])
			sends, hooks := 0, 0
			l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: collabBatchEvidence("amsg_task"), MaxAttempts: attempts, Progress: progress, Select: collabBatchSelect}
			l.BeforeDisposal = func(context.Context, resilience.CollabEvidence) bool { hooks++; return sends > 0 }
			l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				t.Logf("send=%d account=%d action=%s task_cipher=%t", sends, id, a, gjson.GetBytes(b, "input.2.content.1.encrypted_content").Exists())
				if sends == 1 {
					require.Equal(t, resilience.CollabActionReplaceAll, a)
					return collabBatchEvidence(""), false
				}
				return resilience.CollabEvidence{}, true
			}
			delivered := l.Run(context.Background())
			if !delivered && p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding {
				delivered = l.BeforeDisposal(context.Background(), l.LastEvidence)
			}
			t.Logf("attempts=%d sends=%d hooks=%d delivered=%t", attempts, sends, hooks, delivered)
			require.Equal(t, 1, sends, "verified rejection of actual replace-all must enter row (b)")
			require.Equal(t, 2, hooks)
		})
	}
}
