package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type chatContinuationMemoryStore struct {
	*compatMemoryStore
	turns         map[string][]byte
	aliases       map[string]string
	chatRegisters int
}

func newChatContinuationMemoryStore() *chatContinuationMemoryStore {
	return &chatContinuationMemoryStore{compatMemoryStore: newCompatMemoryStore(), turns: map[string][]byte{}, aliases: map[string]string{}}
}
func (s *chatContinuationMemoryStore) LookupChat(_ context.Context, keys []string) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(keys))
	for i, key := range keys {
		out[i] = s.turns[s.aliases[key]]
	}
	return out, nil
}
func (s *chatContinuationMemoryStore) RegisterChat(_ context.Context, turn string, handles []string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.turns[turn]; old != nil && !bytes.Equal(old, value) {
		return AnthropicCompatRegistrationConflict
	}
	for _, handle := range handles {
		if old := s.aliases[handle]; old != "" && old != turn {
			return AnthropicCompatRegistrationConflict
		}
	}
	s.turns[turn] = append([]byte(nil), value...)
	for _, handle := range handles {
		s.aliases[handle] = turn
	}
	s.chatRegisters++
	return nil
}

func chatContinuationFixture(t *testing.T) (*GatewayService, *chatContinuationMemoryStore, *anthropicCompatPlan, []byte, *anthropicCompatChatResponseState) {
	t.Helper()
	store := newChatContinuationMemoryStore()
	s := compatTestService(store.compatMemoryStore)
	s.cache = store
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"private predecessor"}]}`)
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "chat", "")
	require.NoError(t, err)
	scope := compatScope(ctx)
	record := anthropicCompatRecord{Version: 1, AccountID: 71, CredentialRevision: "credential", ProfileRevision: claudeCodeV2CompatProfile.Revision, Model: "claude-opus-5-5", Prefix: "prefix", Beta: "beta", Anchor: scope.Anchor}
	plan := &anthropicCompatPlan{Active: true, Profile: &claudeCodeV2CompatProfile, Scope: scope, Record: record}
	id, err := compatChatRandomID()
	require.NoError(t, err)
	state := &anthropicCompatChatResponseState{blocks: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"private reasoning","signature":"secret-signature"}`), json.RawMessage(`{"type":"tool_use","id":"real-upstream-id","name":"lookup","input":{"q":"test"}}`)}, handles: map[string]string{AnthropicCompatChatHandlePrefix + id: "real-upstream-id"}}
	return s, store, plan, body, state
}

func TestAnthropicCompatChatEncryptionRotationAndImmutableForks(t *testing.T) {
	s, store, plan, body, state := chatContinuationFixture(t)
	ctx := context.Background()
	require.NoError(t, s.commitAnthropicCompatChat(ctx, plan, state))
	require.Len(t, store.turns, 1)
	for _, value := range store.turns {
		require.NotContains(t, string(value), "private")
		require.NotContains(t, string(value), "secret-signature")
		require.NotContains(t, string(value), "real-upstream-id")
	}
	visible, err := compatChatVisibleMessage(state.blocks, state.handles, nil)
	require.NoError(t, err)
	var messages []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(body, "messages").Raw), &messages))
	handle := ""
	for value := range state.handles {
		handle = value
	}
	result, _ := json.Marshal(map[string]string{"role": "tool", "tool_call_id": handle, "content": "result"})
	messages = append(messages, visible, result)
	next, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "messages": messages})
	continued, err := s.PrepareAnthropicCompatibility(ctx, 11, 22, next, "chat", "")
	require.NoError(t, err)
	require.Equal(t, plan.Record, *compatScope(continued).Record)
	parentBytes := make(map[string][]byte)
	for key, value := range store.turns {
		parentBytes[key] = append([]byte(nil), value...)
	}
	childPlan := &anthropicCompatPlan{Active: true, Profile: &claudeCodeV2CompatProfile, Scope: compatScope(continued), Record: plan.Record}
	child := &anthropicCompatChatResponseState{blocks: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"finish","signature":"child-signature"}`), json.RawMessage(`{"type":"text","text":"same visible child"}`)}, handles: map[string]string{}}
	require.NoError(t, s.commitAnthropicCompatChat(ctx, childPlan, child))
	require.Len(t, store.turns, 2)
	require.NoError(t, s.commitAnthropicCompatChat(ctx, childPlan, child), "identical child retries reuse the immutable record")
	require.Len(t, store.turns, 2)
	oldSeed := append([]byte(nil), store.seed...)
	store.previous = oldSeed
	store.seed = bytes.Repeat([]byte{9}, 32)
	continued, err = s.PrepareAnthropicCompatibility(ctx, 11, 22, next, "chat", "")
	require.NoError(t, err)
	childPlan.Scope = compatScope(continued)
	require.NoError(t, s.commitAnthropicCompatChat(ctx, childPlan, child), "a retry after root rotation must reuse the old authenticated child")
	require.Len(t, store.turns, 2)
	childVisible, err := compatChatVisibleMessage(child.blocks, nil, nil)
	require.NoError(t, err)
	thirdMessages := append(append([]json.RawMessage(nil), messages...), childVisible, json.RawMessage(`{"role":"user","content":"next"}`))
	third, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "messages": thirdMessages})
	thirdContext, err := s.PrepareAnthropicCompatibility(ctx, 11, 22, third, "chat", "")
	require.NoError(t, err)
	require.Equal(t, 2, len(compatScope(thirdContext).ChatState.Validated))
	child.blocks[0] = json.RawMessage(`{"type":"thinking","thinking":"different","signature":"different-signature"}`)
	require.ErrorContains(t, s.commitAnthropicCompatChat(ctx, childPlan, child), "Conflicting signed Chat child")
	for key, value := range parentBytes {
		require.Equal(t, value, store.turns[key])
	}
	_, err = s.PrepareAnthropicCompatibility(ctx, 11, 23, next, "chat", "")
	require.ErrorContains(t, err, "Unknown or expired")
	_, err = s.PrepareAnthropicCompatibility(ctx, 12, 22, next, "chat", "")
	require.ErrorContains(t, err, "Unknown or expired")
}

func TestAnthropicCompatChatLargeDeltasAndPrefixChain(t *testing.T) {
	plan := &anthropicCompatPlan{Profile: &claudeCodeV2CompatProfile, Scope: &anthropicCompatScope{Entry: "chat"}}
	state := newAnthropicCompatChatResponseState(plan)
	_, err := state.observe("content_block_start", []byte(`{"index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	require.NoError(t, err)
	chunk := strings.Repeat("x", 128)
	for range 2048 {
		payload, _ := json.Marshal(map[string]any{"index": 0, "delta": map[string]string{"type": "thinking_delta", "thinking": chunk}})
		_, err = state.observe("content_block_delta", payload)
		require.NoError(t, err)
	}
	_, err = state.observe("content_block_delta", []byte(`{"index":0,"delta":{"type":"signature_delta","signature":"signed"}}`))
	require.NoError(t, err)
	_, err = state.observe("content_block_stop", []byte(`{"index":0}`))
	require.NoError(t, err)
	require.Equal(t, 2048*128, len(gjson.GetBytes(state.blocks[0], "thinking").String()))
	require.Equal(t, "signed", gjson.GetBytes(state.blocks[0], "signature").String())
	seed := bytes.Repeat([]byte{1}, 32)
	messages := make([]json.RawMessage, 128)
	for i := range messages {
		raw, _ := json.Marshal(map[string]string{"role": "user", "content": strings.Repeat("large", 1024)})
		messages[i] = raw
	}
	prefixes, err := compatChatPredecessorDigests(seed, messages)
	require.NoError(t, err)
	require.Len(t, prefixes, 129)
	for _, i := range []int{0, 1, 127, 128} {
		digest, e := compatChatDigest(seed, "predecessor", messages[:i])
		require.NoError(t, e)
		require.Equal(t, prefixes[i], digest)
	}
}

func TestAnthropicCompatChatInputAndCiphertextBounds(t *testing.T) {
	s, _, plan, _, state := chatContinuationFixture(t)
	handles := make([]map[string]any, 129)
	for i := range handles {
		id, err := compatChatRandomID()
		require.NoError(t, err)
		handles[i] = map[string]any{"id": AnthropicCompatChatHandlePrefix + id, "type": "function", "function": map[string]string{"name": "lookup", "arguments": "{}"}}
	}
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "tool_calls": handles}}})
	require.NoError(t, err)
	_, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "chat", "")
	require.ErrorContains(t, err, "too many tool handles")
	turnID, err := compatChatRandomID()
	require.NoError(t, err)
	turn := &anthropicCompatChatTurn{Version: 1, ID: turnID, Constraints: plan.Record, Predecessor: "p", Visible: "v", Blocks: state.blocks, Handles: state.handles}
	oversized, _ := json.Marshal(map[string]string{"type": "thinking", "thinking": strings.Repeat("x", AnthropicCompatChatRecordLimit), "signature": "s"})
	turn.Blocks = []json.RawMessage{oversized}
	_, err = compatChatSeal(bytes.Repeat([]byte{1}, 32), plan.Scope, turn)
	require.ErrorContains(t, err, "limit")
}
