//go:build unit

package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabRound2NoticeProvenanceAndAnchor(t *testing.T) {
	s := collabRecordTestService(t)
	text := "Message Type: FINAL_ANSWER\nTask name: task\nSender: child\nPayload:\nGateway notice [new-event]. Quoted Gateway notice [old-event]."
	item := map[string]any{"type": "agent_message", "author": "other", "recipient": "parent", "content": []any{map[string]string{"type": "input_text", "text": text}}}
	body, _ := json.Marshal(map[string]any{"input": []any{item}})
	found := s.ParseCollabSeenNotices(body, map[string]resilience.CollabDigest{"child": "child-thread", "other": "other-thread"})
	for _, notice := range found {
		require.False(t, notice.FinalAnswer)
	}
	item["author"] = "child"
	body, _ = json.Marshal(map[string]any{"input": []any{item}})
	found = s.ParseCollabSeenNotices(body, map[string]resilience.CollabDigest{"child": "child-thread", "parent": "parent-thread"})
	require.Len(t, found, 2)
	require.True(t, found[0].FinalAnswer)
	require.False(t, found[1].FinalAnswer, "a quoted notice cannot authorize borrowing")
}
func TestCollabRound2EncryptedAgentMessageIsInput(t *testing.T) {
	s := collabRecordTestService(t)
	text := "Message Type: FINAL_ANSWER\nTask name: task\nSender: child\nPayload:\nGateway notice [event]. notice"
	items := []any{map[string]any{"type": "agent_message", "content": []any{map[string]string{"type": "input_text", "text": text}}}, map[string]any{"type": "agent_message", "content": []any{map[string]string{"type": "input_text", "text": "envelope"}, map[string]string{"type": "encrypted_content", "encrypted_content": "cipher"}}}}
	body, _ := json.Marshal(map[string]any{"input": items})
	found := s.ParseCollabSeenNotices(body, nil)
	require.Len(t, found, 1)
	require.True(t, found[0].Current)
}

func TestCollabRound2ReceiptsAndRecipient(t *testing.T) {
	s := collabRecordTestService(t)
	notice := "Gateway notice [event]. Earlier work may have taken effect. Quoted Gateway notice [old]."
	child := map[string]any{"type": "agent_message", "author": "child", "recipient": "parent", "content": []any{map[string]string{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: task\nSender: child\nPayload:\n" + notice}}}
	own := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": notice}}}
	body, _ := json.Marshal(map[string]any{"input": []any{own, child, map[string]string{"type": "message", "role": "assistant", "content": "later model output"}}})
	receipts := s.ParseCollabNoticeReceipts(body)
	require.Equal(t, []resilience.CollabDigest{s.CollabDigest(resilience.CollabDigestDomainEvent, "event")}, receipts, "receipt survives later output; quoted event does not qualify")
	members := map[string]resilience.CollabDigest{"child": "child-thread", "parent": "parent-thread", "other": "other-thread"}
	notices := s.ParseCollabSeenNotices(body, members)
	for _, n := range notices {
		require.False(t, n.Current)
	}
	child["recipient"] = "other"
	body, _ = json.Marshal(map[string]any{"input": []any{child}})
	notices = s.ParseCollabSeenNotices(body, members)
	record := resilience.CollabTerminatedRecord{Key: resilience.CollabDeliveryKey{Scope: resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "child-thread", Delivery: "delivery"}, Parent: "parent-thread", Event: receipts[0], ExpiresAt: time.Now().Add(time.Hour)}
	require.Equal(t, resilience.CollabEventSourceSeenOther, resilience.CollabSeenEvent(notices[0], record.Key.Scope, "parent-thread", []resilience.CollabTerminatedRecord{record}, time.Now()).Source)
	var childJSON []byte
	childJSON, _ = json.Marshal(child)
	malformed := []byte(`{"input":[{"type":"agent_message","content":[{"type":"input_text","text":"a","text":"b"}]},` + string(childJSON) + "]}")
	notices = s.ParseCollabSeenNotices(malformed, members)
	for _, n := range notices {
		require.False(t, n.Current)
	}
	require.Empty(t, s.ParseCollabNoticeReceipts(malformed))
	userBody, _ := json.Marshal(map[string]any{"input": []any{map[string]string{"role": "user", "content": notice}}})
	require.Empty(t, s.ParseCollabNoticeReceipts(userBody))
	own["content"] = "quoted " + notice
	body, _ = json.Marshal(map[string]any{"input": []any{own}})
	require.Empty(t, s.ParseCollabNoticeReceipts(body))
}
