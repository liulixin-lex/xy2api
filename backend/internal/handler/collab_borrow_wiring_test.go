//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringBorrowPreservesOriginalBinding(t *testing.T) {
	quietReplayLogs(t)
	settings := resilience.Defaults()
	settings.Collab.Ladder, settings.Collab.LosslessFirst, settings.Collab.RecordRouting, settings.Collab.TreeAffinity = true, true, true, true
	settings.CollaborationPlaintext.Enabled = true
	settings.CollaborationPlaintext.Models = []string{"gpt-5.5"}
	var accounts []int64
	fixture := newCollabWiringFixture(t, settings, func(_ *http.Request, account int64) (*http.Response, error) {
		accounts = append(accounts, account)
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})
	fixture.repo.accounts[0].Extra["openai_collaboration_plaintext"] = false
	second := fixture.repo.accounts[0]
	second.ID, second.Priority = 72, 2
	second.Extra = map[string]any{"openai_collaboration_plaintext": true}
	fixture.repo.accounts = append(fixture.repo.accounts, second)
	declaration := `{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}},"required":["message"],"additionalProperties":false}}]}`
	_, state := fixture.request([]byte(`{"model":"gpt-5.5","tools":[` + declaration + `],"input":[` + replayTask + `,` + replayQuestion + `]}`))
	scope := state.CollabState().Items[0].Key.Scope
	parent := state.CollabState().Thread
	child := fixture.gateway.CollabDigest(resilience.CollabDigestDomainThread, "sample-child")
	require.True(t, fixture.gateway.CollabTreeAffinity().RefreshTreeMembership(context.Background(), resilience.CollabTreeHint{Scope: scope, Thread: child, Parent: parent, AccountID: 71}))
	key := resilience.CollabDeliveryKey{Scope: scope, Thread: child, Item: "item", Delivery: "delivery", Digest: "ciphertext"}
	record := resilience.CollabTerminatedRecord{Key: key, Parent: parent, Event: fixture.gateway.CollabDigest(resilience.CollabDigestDomainEvent, "sample-event"), ExpiresAt: time.Now().Add(time.Hour)}
	_, reserved := fixture.gateway.CollabRecords().ReserveLocalEnding(context.Background(), []resilience.CollabTerminatedRecord{record})
	require.True(t, reserved)
	require.True(t, fixture.gateway.CollabRecords().Terminated(context.Background(), record))
	notice, _ := json.Marshal(map[string]any{"type": "agent_message", "author": "sample-child", "recipient": "sample-thread", "content": []any{map[string]string{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: task\nSender: sample-child\nPayload:\nGateway notice [sample-event]. Unreadable task."}}})
	rec, _ := fixture.request([]byte(`{"model":"gpt-5.5","tools":[` + declaration + `],"input":[` + string(notice) + `,` + replayQuestion + `]}`))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{71, 72}, accounts)
	hint, ok := fixture.gateway.CollabTreeAffinity().LookupTreeHint(context.Background(), scope)
	require.True(t, ok)
	require.EqualValues(t, 71, hint.AccountID)
}
