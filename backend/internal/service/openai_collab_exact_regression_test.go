//go:build unit

package service

import (
	"context"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"strings"
	"testing"
)

func TestCollabExactReferences(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	for _, tc := range []struct{ name, message, param, want string }{
		{"id_control", "Encrypted content hydration failed for item 'amsg_task'.", "", "amsg_task"},
		{"path_control", "Encrypted content could not be decrypted.", "input[2].content[1]", "amsg_task"},
		{"wrong_family_locator", "Encrypted function arguments could not be decrypted.", "input[2].content[1]", ""},
		{"qualified_dot", "Encrypted content could not be decrypted: body.input[2].content[1].", "", ""},
		{"qualified_slash", "Encrypted content could not be decrypted: body/input[2].content[1].", "", ""},
		{"scalar_descendant", "Encrypted content could not be decrypted.", "input[0].encrypted_content.encrypted_content", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "invalid_encrypted_content", "message": tc.message, "param": tc.param}})
			e := p.ResolveEvidence((CollabRejection{Status: 400, Payload: payload}).Evidence(p.Shape))
			sends, hooks := 0, 0
			l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect, BeforeDisposal: func(context.Context, resilience.CollabEvidence) bool { hooks++; return false }, Send: func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				t.Logf("send action=%s task_kept=%t reasoning_kept=%t", a, gjson.GetBytes(b, "input.2.content.1.encrypted_content").Exists(), gjson.GetBytes(b, "input.0.encrypted_content").Exists())
				return resilience.CollabEvidence{}, true
			}}
			l.Run(context.Background())
			t.Logf("named=%+v sends=%d hooks=%d", e.Named, sends, hooks)
			if tc.want == "" {
				require.Zero(t, sends, "malformed whole reference must fall back")
				require.Zero(t, hooks)
			} else {
				require.Equal(t, tc.want, e.Named.Identity)
				require.True(t, e.Named.Located)
				require.Equal(t, 1, sends)
			}
		})
	}
}
func TestCollabExactNamedRepairIsolation(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	n, changed := p.LadderPolicy(collabBatchEvidence("rs_a")).derivation(p, resilience.CollabActionNamedRepair).Derive(p.Original, resilience.CollabActionNamedRepair, "rs_a", false)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(n, "input.0.encrypted_content").Exists())
	require.True(t, gjson.GetBytes(n, "input.1.encrypted_content").Exists())
	require.True(t, gjson.GetBytes(n, "input.2.content.1.encrypted_content").Exists())
}
func TestCollabExactNestedRepairs(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"web_search_preview"}],"input":[` + collabBatchTask + `]}`)
	fixed, _ := sjson.SetBytes(raw, "tools.0.type", "web_search")
	p := ParseCollabBody(raw)
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	l := CollabLadder{Body: p, First: 1, Evidence: collabBatchEvidence("amsg_task"), Progress: progress, MaxAttempts: 4, Select: collabBatchSelect, Send: func(_ context.Context, account int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
		t.Logf("account=%d action=%s tools=%s", account, a, gjson.GetBytes(b, "tools").Raw)
		require.Equal(t, "web_search", gjson.GetBytes(b, "tools.0.type").String())
		return resilience.CollabEvidence{}, true
	}}
	require.True(t, l.Run(context.Background()))
}
func TestCollabExactOverlappingArrayRepairs(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"function","name":"a","bad":true},{"type":"function","name":"b"}],"input":[` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	progress := &CollabLadderProgress{}
	first, _ := sjson.DeleteBytes(raw, "tools.0.bad")
	r := collabObserveDerivedRejection(p, first, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
	progress.RememberRejection(r)
	second, _ := sjson.DeleteBytes(raw, "tools.1")
	r = collabObserveDerivedRejection(p, second, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
	r.AccountID = 2
	progress.RememberRejection(r)
	got := progress.repairs.apply(p, raw)
	t.Logf("repairs=%v tools=%s", progress.repairs, gjson.GetBytes(got, "tools").Raw)
	require.False(t, gjson.GetBytes(got, "tools.0.bad").Exists(), "confirmed subfield removal revived by later parent-array repair")
}
func TestCollabExactIDLessReorder(t *testing.T) {
	raw := []byte(`{"input":[` + strings.ReplaceAll(collabBatchTask, `"id":"amsg_task",`, ``) + `,{"type":"reasoning","encrypted_content":"r"}]}`)
	p := ParseCollabBody(raw)
	require.Empty(t, gjson.GetBytes(raw, "input.0.id").String())
	items := gjson.GetBytes(raw, "input").Array()
	swapped, _ := sjson.SetRawBytes(raw, "input.0", []byte(items[1].Raw))
	swapped, _ = sjson.SetRawBytes(swapped, "input.1", []byte(items[0].Raw))
	r := collabObserveDerivedRejection(p, swapped, `{"error":{"code":"invalid_encrypted_content","param":"input[0]"}}`)
	e := p.ResolveEvidence(r.Evidence(p.Shape))
	t.Logf("resolved=%+v", e.Named)
	if e.Named != nil && e.Named.Located {
		require.Equal(t, "@input:1", e.Named.Identity, "physical reasoning resolved to unrelated original task")
	}
}

func TestCollabExactOrdinaryRejectionLosesState(t *testing.T) {
	for _, code := range []string{"server_error", "rate_limit_exceeded", "context_length_exceeded"} {
		t.Run(code, func(t *testing.T) {
			raw := []byte(`{"truncation":"auto","input":[` + strings.TrimPrefix(collabBatchBody, `{"input":[`))
			p := ParseCollabBody(raw)
			fixed, _ := sjson.DeleteBytes(raw, "truncation")
			fixed, _ = p.Derive(fixed, resilience.CollabActionNamedRepair, "rs_a", false)
			r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"`+code+`","message":"synthetic rejection"}}`)
			progress := &CollabLadderProgress{}
			progress.RememberRejection(r)
			t.Logf("derivation_bytes=%d repairs=%v named_classes=%v", len(r.Derivation), progress.repairs, progress.namedFamilies)
			l := CollabLadder{Body: p, First: 2, Current: 2, Evidence: collabBatchEvidence("rs_b"), Progress: progress, MaxAttempts: 4, Select: func(context.Context, int64, map[int64]bool) (int64, func()) { return 2, func() {} }, Send: func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				t.Logf("action=%s truncation_restored=%t rs_a_restored=%t", a, gjson.GetBytes(b, "truncation").Exists(), gjson.GetBytes(b, "input.0.encrypted_content").Exists())
				require.False(t, gjson.GetBytes(b, "truncation").Exists(), "confirmed field repair lost at ordinary rejection")
				require.False(t, gjson.GetBytes(b, "input.0.encrypted_content").Exists(), "first named reasoning allowance renewed after ordinary rejection")
				return resilience.CollabEvidence{}, true
			}}
			require.True(t, l.Run(context.Background()))
		})
	}
}

func TestCollabAnonymousProtocolRepairIdentity(t *testing.T) {
	raw := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"synthetic tool context"},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, summary, err := normalizeOpenAIFCOBody(raw, DefaultOpenAIFCORules(nil))
	require.NoError(t, err)
	require.Equal(t, 1, summary.Matched)
	items := gjson.GetBytes(fixed, "input").Array()
	fixed, _ = sjson.SetRawBytes(fixed, "input.0", []byte(items[1].Raw))
	fixed, _ = sjson.SetRawBytes(fixed, "input.1", []byte(items[0].Raw))
	rejection := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
	require.False(t, rejection.unlocatable)
	progress := &CollabLadderProgress{}
	progress.RememberRejection(rejection)
	out := progress.repairs.apply(p, raw)
	require.Equal(t, "message", gjson.GetBytes(out, "input.0.type").String())
	require.Equal(t, "amsg_task", gjson.GetBytes(out, "input.1.id").String())
	require.True(t, gjson.GetBytes(out, "input.1.content.1.encrypted_content").Exists())
}
