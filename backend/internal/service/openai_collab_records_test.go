//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) initCollabRecords() { s.CollabRecords() }

func collabRecordTestService(t *testing.T) *OpenAIGatewayService {
	t.Helper()
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-test-secret"}}}
	s.initCollabRecords()
	t.Cleanup(s.CloseCollabRecords)
	return s
}
func collabRecordTestInput() CollabRecordDeliveryInput {
	settings := resilience.Defaults()
	settings.Collab.Ladder = true
	return CollabRecordDeliveryInput{Settings: &settings, HasTools: true, Scope: resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Account: "account", Site: "site", Generation: 1, SendSeq: 1}
}
func collabCallFrame(t *testing.T, args, marker string) []byte {
	t.Helper()
	call := map[string]any{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "call_id": "call", "arguments": args}
	if marker != "" {
		call["encrypted_function_args"] = json.RawMessage(marker)
	}
	b, err := json.Marshal(map[string]any{"type": "response.output_item.done", "item": call})
	require.NoError(t, err)
	return b
}
func TestCollabRecordFinalFramesAndDecodedDigests(t *testing.T) {
	for _, marker := range []string{"", "null", `["message"]`, "[]"} {
		t.Run("marker_"+marker, func(t *testing.T) {
			s := collabRecordTestService(t)
			in := collabRecordTestInput()
			message := "  quotes \" slash \\ newline\n世界 "
			arguments, _ := json.Marshal(map[string]string{"message": message})
			frame := collabCallFrame(t, string(arguments), marker)
			consumer := s.NewCollabRecordDelivery(in)
			require.NotNil(t, consumer)
			hooks := &resilience.CollabDeliveryHooks{}
			hooks.Register(consumer)
			ctx := context.Background()
			e := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, Executable: true, EventType: "response.output_item.done"}
			inbound, _ := json.Marshal(map[string]string{"encrypted_content": message})
			key := resilience.CollabDeliveryKey{Scope: in.Scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: s.CollabDigest(resilience.CollabDigestDomainCiphertext, gjson.GetBytes(inbound, "encrypted_content").String())}
			q := resilience.CollabRecordQuery{Key: key, Call: s.CollabDigest(resilience.CollabDigestDomainCall, "call")}
			batch := resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{q}, Records: true}
			hooks.Received(ctx, e, frame)
			require.Empty(t, s.collabRecords.LookupBatch(ctx, batch).Records[q])
			hooks.AboutToWrite(ctx, e, frame)
			before := s.collabRecords.LookupBatch(ctx, batch).Records[q]
			if marker != "[]" {
				require.Equal(t, resilience.CollabIssuedStatusPending, before.Issued[0].Status)
			} else {
				require.Empty(t, before)
			}
			recorder := httptest.NewRecorder()
			_, err := recorder.Write(frame)
			require.NoError(t, err)
			recorder.Flush()
			hooks.Written(ctx, e, frame)
			require.Equal(t, frame, recorder.Body.Bytes())
			result := s.collabRecords.LookupBatch(ctx, batch).Records[q]
			if marker == "[]" {
				require.Empty(t, result.Issued)
				require.Len(t, result.Plaintext, 1)
				require.Equal(t, s.CollabDigest(resilience.CollabDigestDomainArguments, string(arguments)), result.Plaintext[0].Arguments)
			} else {
				require.Len(t, result.Issued, 1)
				require.Empty(t, result.Plaintext)
				require.Equal(t, key.Digest, result.Issued[0].Digest)
				require.Equal(t, resilience.CollabIssuedStatusConfirmed, result.Issued[0].Status)
				require.Equal(t, marker == `["message"]`, result.Issued[0].ExplicitlyEncrypted)
			}
			var item map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(frame, &item))
			response := map[string]any{"status": "completed", "output": []json.RawMessage{item["item"]}}
			for _, kind := range []string{"response.completed", "response.done", "response.json"} {
				var payload []byte
				if kind == "response.json" {
					payload, _ = json.Marshal(response)
				} else {
					payload, _ = json.Marshal(map[string]any{"response": response})
				}
				e.EventType = kind
				e.Sequence++
				hooks.AboutToWrite(ctx, e, payload)
				hooks.Written(ctx, e, payload)
			}
			require.Equal(t, result, s.collabRecords.LookupBatch(ctx, batch).Records[q])
			e.Successful = true
			hooks.Completed(ctx, e, nil)
			s.RecordCollabConsumed(ctx, in.Settings, true, in.Account, []resilience.CollabDeliveryKey{key})
			require.Len(t, s.collabRecords.LookupBatch(ctx, batch).Records[q].Consumed, 1)
		})
	}
}
func TestCollabRecordFailedWritesAndGenerations(t *testing.T) {
	for _, kind := range []string{"write_error", "aborted", "mismatched_bytes", "wrong_generation"} {
		t.Run(kind, func(t *testing.T) {
			s := collabRecordTestService(t)
			in := collabRecordTestInput()
			consumer := s.NewCollabRecordDelivery(in)
			frame := collabCallFrame(t, `{"message":"cipher"}`, "")
			ctx := context.Background()
			event := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, Executable: true, EventType: "response.output_item.done", Payload: string(frame)}
			consumer.AboutToWrite(ctx, event)
			switch kind {
			case "write_error":
				event.WriteError = errors.New("synthetic")
				consumer.Written(ctx, event)
			case "mismatched_bytes":
				event.Payload = string(collabCallFrame(t, `{"message":"changed"}`, ""))
				consumer.Written(ctx, event)
			case "wrong_generation":
				event.Generation = 2
				consumer.Written(ctx, event)
				event.Generation = 1
			}
			consumer.Completed(ctx, event)
			query := resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: in.Scope}, Call: s.CollabDigest(resilience.CollabDigestDomainCall, "call")}
			lookup := s.collabRecords.LookupBatch(ctx, resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{query}, Records: true}).Records[query]
			require.Len(t, lookup.Issued, 1)
			require.Equal(t, resilience.CollabIssuedStatusVoid, lookup.Issued[0].Status)
		})
	}
}

type collabDeliverySharedFailure struct {
	mode  string
	calls atomic.Int64
}

func (s *collabDeliverySharedFailure) Apply(ctx context.Context, _ resilience.CollabRecordMutation) error {
	s.calls.Add(1)
	switch s.mode {
	case "panic":
		panic("synthetic")
	case "slow":
		<-ctx.Done()
	}
	return errors.New("synthetic")
}
func (s *collabDeliverySharedFailure) LookupBatch(ctx context.Context, _ resilience.CollabRecordBatch) (resilience.CollabRecordBatchResult, error) {
	switch s.mode {
	case "panic":
		panic("synthetic")
	case "slow":
		<-ctx.Done()
	}
	return resilience.CollabRecordBatchResult{}, errors.New("synthetic")
}
func TestCollabRecordStoreFailuresPreserveFinalBytesAndOutcome(t *testing.T) {
	for _, mode := range []string{"absent", "slow", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			s := collabRecordTestService(t)
			s.collabRecords.Close()
			var shared resilience.CollabSharedRecordsStore
			if mode != "absent" {
				shared = &collabDeliverySharedFailure{mode: mode}
			}
			s.collabRecords = resilience.NewSafeCollabRecordsStore(resilience.NewMemoryCollabRecordsStore(), shared, 32)
			in := collabRecordTestInput()
			hooks := &resilience.CollabDeliveryHooks{}
			consumer := s.NewCollabRecordDelivery(in)
			require.NotNil(t, consumer)
			hooks.Register(consumer)
			frame := collabCallFrame(t, `{"message":"cipher"}`, "")
			event := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, Executable: true, EventType: "response.output_item.done", Successful: true}
			output := httptest.NewRecorder()
			ctx := context.Background()
			start := time.Now()
			hooks.AboutToWrite(ctx, event, frame)
			_, err := output.Write(frame)
			output.Flush()
			event.WriteError = err
			hooks.Written(ctx, event, frame)
			hooks.Completed(ctx, event, nil)
			require.NoError(t, err)
			require.Equal(t, 200, output.Code)
			require.Equal(t, frame, output.Body.Bytes())
			require.Less(t, time.Since(start), 100*time.Millisecond)
			budget, cancel := resilience.WithCollabRecordReadBudget(ctx)
			defer cancel()
			start = time.Now()
			s.collabRecords.LookupBatch(budget, resilience.CollabRecordBatch{Scope: in.Scope, Records: true})
			require.Less(t, time.Since(start), 500*time.Millisecond)
			require.Equal(t, frame, output.Body.Bytes())
		})
	}
}
func TestCollabRecordsDisabledAndConsumedCompletionGate(t *testing.T) {
	s := collabRecordTestService(t)
	in := collabRecordTestInput()
	in.Settings.Collab.Records = false
	require.Nil(t, s.NewCollabRecordDelivery(in))
	key := resilience.CollabDeliveryKey{Scope: in.Scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
	s.RecordCollabConsumed(context.Background(), in.Settings, true, in.Account, []resilience.CollabDeliveryKey{key})
	in.Settings.Collab.Records = true
	s.RecordCollabConsumed(context.Background(), in.Settings, false, in.Account, []resilience.CollabDeliveryKey{key})
	q := resilience.CollabRecordQuery{Key: key}
	require.Empty(t, s.collabRecords.LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{q}, Records: true}).Records[q].Consumed)
	in.HasTools = false
	in.AgentCiphertext = false
	require.Zero(t, collabMinAllocsPerRun(100, func() { require.Nil(t, s.NewCollabRecordDelivery(in)) }))
}
func TestCollabSeenNoticeNeedsRegisteredParentAndSender(t *testing.T) {
	s := collabRecordTestService(t)
	scope := collabRecordTestInput().Scope
	text := "Message Type: FINAL_ANSWER\nTask name: synthetic-task\nSender: child\nPayload:\nGateway notice [synthetic-event]. The task content could not be read."
	body, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"type": "agent_message", "author": "child", "recipient": "parent", "content": []any{map[string]string{"type": "input_text", "text": text}}}}})
	notices := s.ParseCollabSeenNotices(body, map[string]resilience.CollabDigest{"child": "child-thread", "parent": "parent-thread"})
	require.Len(t, notices, 1)
	require.True(t, notices[0].FinalAnswer)
	require.True(t, notices[0].Current)
	record := resilience.CollabTerminatedRecord{Key: resilience.CollabDeliveryKey{Scope: scope, Thread: "child-thread", Delivery: "delivery"}, Event: s.CollabDigest(resilience.CollabDigestDomainEvent, "synthetic-event"), Parent: "parent-thread", ExpiresAt: time.Now().Add(time.Hour)}
	seen := resilience.CollabSeenEvent(notices[0], scope, "parent-thread", []resilience.CollabTerminatedRecord{record}, time.Now())
	require.Equal(t, resilience.CollabEventSourceSeenParentNotice, seen.Source)
	require.Equal(t, resilience.CollabEventSourceSeenOther, resilience.CollabSeenEvent(notices[0], scope, "other", []resilience.CollabTerminatedRecord{record}, time.Now()).Source)
	unknown := s.ParseCollabSeenNotices(body, nil)
	require.Equal(t, resilience.CollabEventSourceSeenOther, resilience.CollabSeenEvent(unknown[0], scope, "parent-thread", []resilience.CollabTerminatedRecord{record}, time.Now()).Source)
	for _, changed := range [][]byte{
		bytes.Replace(body, []byte("agent_message"), []byte("message"), 1),
		bytes.Replace(body, []byte("FINAL_ANSWER"), []byte("MESSAGE"), 1),
		bytes.Replace(body, []byte("Payload:"), []byte("Payload: ambiguous"), 1),
	} {
		parsed := s.ParseCollabSeenNotices(changed, map[string]resilience.CollabDigest{"child": "child-thread", "parent": "parent-thread"})
		require.Len(t, parsed, 1)
		require.False(t, parsed[0].FinalAnswer)
	}
	require.Equal(t, resilience.CollabEventSourceSeenOther, resilience.CollabSeenEvent(notices[0], scope, "parent-thread", nil, time.Now()).Source)
	var historical map[string]any
	require.NoError(t, json.Unmarshal(body, &historical))
	inputs, ok := historical["input"].([]any)
	require.True(t, ok)
	historical["input"] = append(inputs, map[string]string{"role": "assistant", "content": "acknowledged"})
	history, _ := json.Marshal(historical)
	old := s.ParseCollabSeenNotices(history, map[string]resilience.CollabDigest{"child": "child-thread", "parent": "parent-thread"})
	require.Len(t, old, 1)
	require.True(t, old[0].FinalAnswer)
	require.False(t, old[0].Current)

}

var collabOrdinarySink *CollabRecordDelivery

func BenchmarkCollabRecordsOrdinaryRequest(b *testing.B) {
	s := &OpenAIGatewayService{}
	s.initCollabRecords()
	defer s.CloseCollabRecords()
	in := collabRecordTestInput()
	in.HasTools = false
	payload := []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		b.Run(name, func(b *testing.B) {
			in.Settings.Collab.Records = enabled
			b.ReportAllocs()
			for b.Loop() {
				collabOrdinarySink = s.NewCollabRecordDelivery(in)
				_, _ = io.Discard.Write(payload)
			}
		})
	}
}

func TestCollabRecordAccountSurvivesCredentialRefresh(t *testing.T) {
	s := collabRecordTestService(t)
	source := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "synthetic-target", "access_token": "synthetic-old"}}
	account, site := s.CollabRecordAccount(source.ID, source.GetOpenAIBaseURL())
	source.Credentials["access_token"] = "synthetic-new"
	refreshed, refreshedSite := s.CollabRecordAccount(source.ID, source.GetOpenAIBaseURL())
	require.Equal(t, account, refreshed)
	require.Equal(t, site, refreshedSite)
	moved, _ := s.CollabRecordAccount(source.ID, "other-target")
	require.NotEqual(t, account, moved)
	other, sameSite := s.CollabRecordAccount(8, source.GetOpenAIBaseURL())
	require.NotEqual(t, account, other)
	require.Equal(t, site, sameSite)
}

func TestCollabRecordAllFinalExitsAndPlaintextWriteFailure(t *testing.T) {
	for _, kind := range []string{"response.output_item.done", "response.completed", "response.json"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed_%t", kind, failed), func(t *testing.T) {
				s := collabRecordTestService(t)
				in := collabRecordTestInput()
				consumer := s.NewCollabRecordDelivery(in)
				itemFrame := collabCallFrame(t, `{"message":"readable task"}`, "[]")
				var data map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(itemFrame, &data))
				payload := itemFrame
				response := map[string]any{"status": "completed", "output": []json.RawMessage{data["item"]}}
				if kind == "response.completed" {
					payload, _ = json.Marshal(map[string]any{"response": response})
				}
				if kind == "response.json" {
					payload, _ = json.Marshal(response)
				}
				if kind != "response.json" {
					payload = append(append([]byte("event: "+kind+"\ndata: "), payload...), []byte("\n\n")...)
				}
				event := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, Executable: true, EventType: kind, Payload: string(payload)}
				consumer.AboutToWrite(context.Background(), event)
				if failed {
					event.WriteError = errors.New("synthetic")
				}
				consumer.Written(context.Background(), event)
				consumer.Completed(context.Background(), event)
				q := resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: in.Scope}, Call: s.CollabDigest(resilience.CollabDigestDomainCall, "call")}
				records := s.collabRecords.LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: in.Scope, Queries: []resilience.CollabRecordQuery{q}, Records: true}).Records[q]
				if failed {
					require.Empty(t, records.Plaintext)
				} else {
					require.Len(t, records.Plaintext, 1)
				}
			})
		}
	}
}
