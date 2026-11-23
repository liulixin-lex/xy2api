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

func TestCollabIdentityNonstringProse(t *testing.T) {
	p := ParseCollabBody([]byte(collabBatchBody))
	for _, value := range []any{true, 123, nil, []any{"input[1]"}, map[string]any{"at": "input[1]"}} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "invalid_encrypted_content", "item_id": "rs_a", "message": value}})
			e := p.ResolveEvidence((CollabRejection{Payload: raw, Status: 400}).Evidence(p.Shape))
			require.NotNil(t, e.Named)
			require.False(t, e.Named.Located)
		})
	}
}
func TestCollabIdentityNoEditableEndpoint(t *testing.T) {
	p := ParseCollabBody([]byte(`{"input":[{"type":"function_call","id":"fc_a","name":"f","arguments":"{}","encrypted_function_args":["opaque"]}]}`))
	e := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
		t.Fatal("unchanged body must not be sent")
		return e, false
	}}
	require.False(t, l.Run(context.Background()))
	t.Logf("stop=%d local=%t", l.StopReason, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
	require.Equal(t, CollabLadderFallback, l.StopReason)
	require.False(t, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
}
func TestCollabIdentityAnonymousArrayRemoval(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"delete"},{"type":"input_text","text":"keep"}]},` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	fixed, err := sjson.DeleteBytes(raw, "input.0.content.0")
	require.NoError(t, err)
	r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"server_error"}}`)
	r.AccountID = 1
	progress := &CollabLadderProgress{}
	progress.RememberRejection(r)
	got := progress.repairs.apply(p, raw)
	require.JSONEq(t, string(fixed), string(got))
}
func TestCollabIdentityRealAnonymousArgumentsRepair(t *testing.T) {
	for _, identified := range []bool{false, true} {
		for _, dropStatus := range []bool{false, true} {
			t.Run(fmt.Sprintf("id_%t/drop_%t", identified, dropStatus), func(t *testing.T) {
				id := ""
				if identified {
					id = `"id":"ts_1",`
				}
				raw := []byte(`{"input":[{"type":"tool_search_call",` + id + `"call_id":"call_search","arguments":"{\"query\":\"x\"}","status":"completed"},` + collabBatchTask + `]}`)
				p := ParseCollabBody(raw)
				fixed, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(400, raw, []byte(`{"error":{"code":"invalid_type","message":"Invalid type for 'input[0].arguments': expected an object, but got a string instead.","param":"input[0].arguments"}}`))
				require.NoError(t, err)
				require.True(t, changed)
				require.True(t, gjson.GetBytes(fixed, "input.0.arguments").IsObject())
				if dropStatus {
					var ch bool
					fixed, _, ch, err = normalizeOpenAIResponsesRejectedFieldRetryBody(400, fixed, []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[0].status'.","param":"input[0].status"}}`))
					require.NoError(t, err)
					require.True(t, ch)
				}
				r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
				r.AccountID = 1
				progress := &CollabLadderProgress{}
				progress.RememberRejection(r)
				got := progress.repairs.apply(p, raw)
				e := r.Evidence(p.Shape)
				sends := 0
				l := CollabLadder{Body: p, First: 1, Current: 1, Progress: progress, Evidence: e, MaxAttempts: 4, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
					sends++
					return resilience.CollabEvidence{}, true
				}}
				delivered := l.Run(context.Background())
				t.Logf("reason=%s unlocatable=%t repairs=%v arguments_type=%s sends=%d delivered=%t", reason, r.unlocatable, progress.repairs, gjson.GetBytes(got, "input.0.arguments").Type, sends, delivered)
				require.False(t, r.unlocatable, "real field repair must retain original identity")
				require.JSONEq(t, string(fixed), string(got))
				require.True(t, delivered)
				require.Equal(t, 1, sends)
			})
		}
	}
}
func TestCollabIdentityDuplicateLineageReorder(t *testing.T) {
	task, _ := sjson.Delete(collabBatchTask, "id")
	raw := []byte(`{"input":[` + task + `,` + task + `]}`)
	p := ParseCollabBody(raw)
	fixed, changed := p.Derive(raw, resilience.CollabActionNamedRepair, "@input:0", false)
	require.True(t, changed)
	a := gjson.GetBytes(fixed, "input.0").Raw
	b := gjson.GetBytes(fixed, "input.1").Raw
	moved, _ := sjson.SetRawBytes(fixed, "input.0", []byte(b))
	moved, _ = sjson.SetRawBytes(moved, "input.1", []byte(a))
	positions, ok := p.inputPositions(moved)
	t.Logf("positions=%v valid=%t", positions, ok)
	require.True(t, ok)
	require.Equal(t, map[int]int{0: 1, 1: 0}, positions, "known transformed occurrence must retain original identity after reorder")
}

func TestCollabIdentityLegacyAnonymousReplacement(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			raw := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"first"},{"type":"encrypted_content","encrypted_content":"second"}]}]}`)
			p := ParseCollabBody(raw)
			slots, _, err := openAIM3Slots(raw)
			require.NoError(t, err)
			require.Len(t, slots, 2)
			state := &openAIM3Account{replaced: map[openAIM3SlotID]struct{}{slots[0].id: {}}}
			if all {
				state.replaced[slots[1].id] = struct{}{}
			}
			attempt := &openAIM3Attempt{service: &OpenAIGatewayService{}, request: &openAIM3Request{}, state: state, account: &Account{ID: 1}}
			fixed, err := attempt.derive(context.Background(), raw)
			require.NoError(t, err)
			r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_encrypted_content"}}`)
			r.AccountID = 1
			require.False(t, r.unlocatable)
			positions, ok := p.inputPositions(fixed)
			require.True(t, ok)
			require.Equal(t, map[int]int{0: 0}, positions)
			if all {
				progress := &CollabLadderProgress{}
				progress.RememberRejection(r)
				l := CollabLadder{Body: p, First: 1, Progress: progress, Evidence: r.Evidence(p.Shape), MaxAttempts: 1, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
					t.Fatal("already disposed")
					return resilience.CollabEvidence{}, false
				}}
				require.False(t, l.Run(context.Background()))
				require.Equal(t, CollabLadderFinalDisposal, l.StopReason)
			}
		})
	}
}
