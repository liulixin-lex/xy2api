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

func TestCollabWiringBorrowWithoutTreeAffinity(t *testing.T) {
	quietReplayLogs(t)
	for _, name := range []string{"valid", "records_off", "wrong_sender", "wrong_parent", "wrong_recipient", "wrong_event", "historical"} {
		t.Run(name, func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Collab.Ladder, settings.Collab.LosslessFirst, settings.Collab.RecordRouting, settings.Collab.TreeAffinity = true, true, true, false
			settings.Collab.Records = name != "records_off"
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
			// No affinity registration: tree_affinity is off; termination itself authenticates parent and child.
			key := resilience.CollabDeliveryKey{Scope: scope, Thread: child, Item: "item", Delivery: "delivery", Digest: "ciphertext"}
			record := resilience.CollabTerminatedRecord{Key: key, Parent: parent, Event: fixture.gateway.CollabDigest(resilience.CollabDigestDomainEvent, "sample-event"), ExpiresAt: time.Now().Add(time.Hour)}
			if name == "wrong_sender" {
				record.Key.Thread = "other-child"
			}
			if name == "wrong_parent" {
				record.Parent = "other-parent"
			}
			if name == "wrong_event" {
				record.Event = "other-event"
			}
			_, reserved := fixture.gateway.CollabRecords().ReserveLocalEnding(context.Background(), []resilience.CollabTerminatedRecord{record})
			require.True(t, reserved)
			require.True(t, fixture.gateway.CollabRecords().Terminated(context.Background(), record))
			recipient := "sample-thread"
			if name == "wrong_recipient" {
				recipient = "other-parent"
			}
			notice, _ := json.Marshal(map[string]any{"type": "agent_message", "author": "sample-child", "recipient": recipient, "content": []any{map[string]string{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: task\nSender: sample-child\nPayload:\nGateway notice [sample-event]. Unreadable task."}}})
			if name == "historical" {
				notice = append(notice, []byte(`,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"already handled"}]}`)...)
			}
			rec, _ := fixture.request([]byte(`{"model":"gpt-5.5","tools":[` + declaration + `],"input":[` + string(notice) + `,` + replayQuestion + `]}`))
			require.Equal(t, http.StatusOK, rec.Code)
			t.Logf("tree_affinity=%t borrow_gate=%t accounts=%v", settings.Collab.TreeAffinity, settings.CollabRedispatchBorrowOn(), accounts)
			expected := []int64{71, 71}
			if name == "valid" || name == "records_off" {
				expected[1] = 72
			}
			require.Equal(t, expected, accounts)
		})
	}
}
