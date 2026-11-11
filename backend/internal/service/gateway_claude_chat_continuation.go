package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Chat clients round-trip standard tool call IDs, but commonly discard reasoning.
// V2 IDs are unguessable references to one immutable assistant turn. Only that
// assistant's encrypted blocks and predecessor/visible-message digests are stored.
const AnthropicCompatChatHandlePrefix = "ccv2_"
const AnthropicCompatChatKeyPrefix = "anthropic_compat:chat:v2:"
const AnthropicCompatChatRecordLimit = 1 << 20
const anthropicCompatChatMessageLimit = 1024

// RegisterChat atomically installs one immutable encrypted turn and all its
// handle aliases. Re-registering the identical value refreshes its retention.
// LookupChat resolves aliases, returning nil for missing/expired turns.
type AnthropicCompatChatStore interface {
	AnthropicCompatProvenanceStore
	LookupChat(context.Context, []string) ([][]byte, error)
	RegisterChat(context.Context, string, []string, []byte, time.Duration) error
}

type anthropicCompatChatTurn struct {
	Version     int                   `json:"v"`
	ID          string                `json:"id"`
	Constraints anthropicCompatRecord `json:"constraints"`
	Predecessor string                `json:"predecessor"`
	Visible     string                `json:"visible"`
	Blocks      []json.RawMessage     `json:"blocks"`
	Handles     map[string]string     `json:"handles"`
	Parent      string                `json:"parent,omitempty"`
}

type anthropicCompatChatRead struct {
	Turn      *anthropicCompatChatTurn
	Seed      []byte
	Encrypted []byte
}

type anthropicCompatChatRequestState struct {
	Messages           []json.RawMessage
	Restores           map[string]*anthropicCompatChatRead
	Validated          map[int]*anthropicCompatChatRead
	Reads              []*anthropicCompatChatRead
	LastTurn           *anthropicCompatChatRead
	UnmanagedReasoning bool
	ResponseRewrite    *ToolNameRewrite
}

func compatChatHistoryError(message string) error {
	return compatInvalid("anthropic_compat_history_incompatible", message, AnthropicCompatReasonConstraints)
}

// This policy is evaluated only after resolving an account's compatibility
// profile. Ordinary accounts keep their existing Chat conversion behavior.
func compatChatProfileContinuationError(scope *anthropicCompatScope, profile *AnthropicCompatProfile) error {
	if scope == nil || profile == nil || !scope.ChatContinuation {
		return nil
	}
	if profile.ID != AnthropicCompatProfileClaudeCodeV2 {
		return compatChatContinuationError()
	}
	if scope.ChatState != nil && scope.ChatState.UnmanagedReasoning {
		return compatInvalid("anthropic_compat_request_unsupported", "Chat signed reasoning requires the original gateway tool call handles", AnthropicCompatReasonChatContinuation)
	}
	return nil
}

func compatChatRandomID() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func compatChatValidID(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(value) == 32 && len(decoded) == 24 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func compatChatKey(seed []byte, scope *anthropicCompatScope, kind, id string) string {
	return AnthropicCompatChatKeyPrefix + kind + ":" + compatDigest(seed, "chat-v2-index", strconv.FormatInt(scope.UserID, 10), strconv.FormatInt(scope.KeyID, 10), kind, id)
}

func compatChatChildKey(seed []byte, scope *anthropicCompatScope, parent, predecessor, visible string) string {
	return compatChatKey(seed, scope, "c", compatDigest(seed, "chat-v2-child", parent, predecessor, visible))
}

func compatChatTurnAliases(seed []byte, scope *anthropicCompatScope, turn *anthropicCompatChatTurn) []string {
	keys := make([]string, 0, len(turn.Handles)+1)
	for handle := range turn.Handles {
		keys = append(keys, compatChatKey(seed, scope, "h", handle))
	}
	if len(keys) == 0 && turn.Parent != "" {
		keys = append(keys, compatChatChildKey(seed, scope, turn.Parent, turn.Predecessor, turn.Visible))
	}
	return keys
}

func compatChatAEAD(seed []byte) (cipher.AEAD, error) {
	if len(seed) < 32 {
		return nil, fmt.Errorf("invalid compatibility encryption key")
	}
	key, _ := hex.DecodeString(compatDigest(seed, "xy2api:claude-chat-continuation:v2:aes-256-gcm"))
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func compatChatAAD(scope *anthropicCompatScope, id string) []byte {
	return []byte("xy2api:claude-chat-continuation:v2:" + strconv.FormatInt(scope.UserID, 10) + ":" + strconv.FormatInt(scope.KeyID, 10) + ":" + id)
}

func compatChatSeal(seed []byte, scope *anthropicCompatScope, turn *anthropicCompatChatTurn) ([]byte, error) {
	aead, err := compatChatAEAD(seed)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(turn)
	if err != nil || len(plain)+1+32+aead.NonceSize()+aead.Overhead() > AnthropicCompatChatRecordLimit {
		return nil, compatUnavailable("Chat continuation exceeds the encrypted turn limit")
	}
	value := make([]byte, 1+32)
	value[0] = 1
	copy(value[1:33], turn.ID)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	value = append(value, nonce...)
	return aead.Seal(value, nonce, plain, compatChatAAD(scope, turn.ID)), nil
}

func compatChatOpen(seed []byte, scope *anthropicCompatScope, value []byte) (*anthropicCompatChatTurn, error) {
	aead, err := compatChatAEAD(seed)
	if err != nil || len(value) < 33+12+16 || len(value) > AnthropicCompatChatRecordLimit || value[0] != 1 {
		return nil, compatChatHistoryError("Invalid encrypted Chat continuation")
	}
	id := string(value[1:33])
	if !compatChatValidID(id) {
		return nil, compatChatHistoryError("Invalid encrypted Chat continuation")
	}
	plain, err := aead.Open(nil, value[33:33+aead.NonceSize()], value[33+aead.NonceSize():], compatChatAAD(scope, id))
	if err != nil {
		return nil, compatChatHistoryError("Chat continuation authentication failed")
	}
	var turn anthropicCompatChatTurn
	if json.Unmarshal(plain, &turn) != nil || turn.Version != 1 || turn.ID != id || !validAnthropicCompatRecord(turn.Constraints) || turn.Predecessor == "" || turn.Visible == "" || len(turn.Blocks) == 0 || (len(turn.Handles) == 0 && !compatChatValidID(turn.Parent)) || len(turn.Handles) > anthropicCompatSignatureLimit {
		return nil, compatChatHistoryError("Invalid Chat continuation record")
	}
	for handle, upstreamID := range turn.Handles {
		if !strings.HasPrefix(handle, AnthropicCompatChatHandlePrefix) || !compatChatValidID(strings.TrimPrefix(handle, AnthropicCompatChatHandlePrefix)) || upstreamID == "" {
			return nil, compatChatHistoryError("Invalid Chat continuation handle")
		}
	}
	return &turn, nil
}

// Canonicalization removes only non-round-tripped reasoning and stream indexes.
// The full visible predecessor, including every prior tool output, remains bound.
func compatChatCanonicalMessage(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var msg map[string]any
	if err := decoder.Decode(&msg); err != nil || msg == nil {
		return nil, compatChatHistoryError("Invalid Chat history message")
	}
	if msg["role"] == "assistant" {
		delete(msg, "reasoning")
		delete(msg, "reasoning_content")
		if msg["content"] == nil || msg["content"] == "" {
			delete(msg, "content")
		}
		if msg["refusal"] == nil || msg["refusal"] == "" {
			delete(msg, "refusal")
		}
		if calls, ok := msg["tool_calls"].([]any); ok {
			for _, item := range calls {
				call, ok := item.(map[string]any)
				if !ok {
					return nil, compatChatHistoryError("Invalid Chat tool call")
				}
				delete(call, "index")
				fn, ok := call["function"].(map[string]any)
				if !ok {
					return nil, compatChatHistoryError("Invalid Chat tool function")
				}
				arguments, ok := fn["arguments"].(string)
				if !ok {
					return nil, compatChatHistoryError("Invalid Chat tool arguments")
				}
				var parsed any
				argDecoder := json.NewDecoder(strings.NewReader(arguments))
				argDecoder.UseNumber()
				if !json.Valid([]byte(arguments)) || argDecoder.Decode(&parsed) != nil {
					return nil, compatChatHistoryError("Invalid Chat tool arguments")
				}
				canonical, _ := json.Marshal(parsed)
				fn["arguments"] = string(canonical)
			}
		}
	}
	return json.Marshal(msg)
}

func compatChatDigest(seed []byte, domain string, messages []json.RawMessage) (string, error) {
	if domain == "predecessor" {
		prefixes, err := compatChatPredecessorDigests(seed, messages)
		if err != nil {
			return "", err
		}
		return prefixes[len(prefixes)-1], nil
	}
	canonical := make([]json.RawMessage, 0, len(messages))
	for _, raw := range messages {
		msg, err := compatChatCanonicalMessage(raw)
		if err != nil {
			return "", err
		}
		canonical = append(canonical, msg)
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return compatDigest(seed, "chat-v2-"+domain, string(raw)), nil
}

// Every prefix is authenticated as an ordered, length-delimited chain. Compute
// them once per root key so a long user prompt is not re-serialized per turn.
func compatChatPredecessorDigests(seed []byte, messages []json.RawMessage) ([]string, error) {
	if len(messages) > anthropicCompatChatMessageLimit {
		return nil, compatInvalid("anthropic_compat_history_limit", "Chat continuation input exceeds its bounded message limit")
	}
	out := make([]string, len(messages)+1)
	out[0] = compatDigest(seed, "chat-v2-predecessor-start")
	for i, raw := range messages {
		canonical, err := compatChatCanonicalMessage(raw)
		if err != nil {
			return nil, err
		}
		out[i+1] = compatDigest(seed, "chat-v2-predecessor-step", out[i], string(canonical))
	}
	return out, nil
}

func compatChatMessageReasoning(msg gjson.Result) bool {
	if msg.Get("role").String() != "assistant" {
		return false
	}
	if msg.Get("reasoning_content").String() != "" || msg.Get("reasoning").String() != "" {
		return true
	}
	for _, block := range msg.Get("content").Array() {
		switch block.Get("type").String() {
		case "thinking", "redacted_thinking", "reasoning":
			return true
		}
	}
	return false
}

func (s *GatewayService) prepareAnthropicCompatChat(ctx context.Context, scope *anthropicCompatScope, body []byte) error {
	state := &anthropicCompatChatRequestState{Restores: make(map[string]*anthropicCompatChatRead), Validated: make(map[int]*anthropicCompatChatRead)}
	scope.ChatState = state
	if err := json.Unmarshal([]byte(gjson.GetBytes(body, "messages").Raw), &state.Messages); err != nil {
		return compatChatHistoryError("Invalid Chat messages")
	}
	handles := make([]string, 0)
	seen := map[string]bool{}
	outputRefs := map[string]int{}
	for _, raw := range state.Messages {
		msg := gjson.ParseBytes(raw)
		ids := []string{msg.Get("tool_call_id").String()}
		if outputID := msg.Get("tool_call_id").String(); strings.HasPrefix(outputID, AnthropicCompatChatHandlePrefix) {
			if msg.Get("role").String() != "tool" {
				return compatChatHistoryError("Gateway Chat handles require a tool output message")
			}
			outputRefs[outputID]++
		}
		managed := false
		for _, call := range msg.Get("tool_calls").Array() {
			ids = append(ids, call.Get("id").String())
			managed = managed || strings.HasPrefix(call.Get("id").String(), AnthropicCompatChatHandlePrefix)
		}
		state.UnmanagedReasoning = state.UnmanagedReasoning || (!managed && compatChatMessageReasoning(msg))
		for _, id := range ids {
			if !strings.HasPrefix(id, AnthropicCompatChatHandlePrefix) {
				continue
			}
			if !compatChatValidID(strings.TrimPrefix(id, AnthropicCompatChatHandlePrefix)) {
				return compatChatHistoryError("Invalid gateway Chat tool call handle")
			}
			if !seen[id] {
				seen[id] = true
				handles = append(handles, id)
			}
		}
	}
	if len(handles) == 0 {
		return nil
	}
	if len(body) > 8*AnthropicCompatChatRecordLimit || len(state.Messages) > anthropicCompatChatMessageLimit {
		return compatInvalid("anthropic_compat_history_limit", "Chat continuation input exceeds its bounded lookup limit")
	}
	if len(handles) > anthropicCompatSignatureLimit {
		return compatInvalid("anthropic_compat_history_limit", "Chat continuation has too many tool handles")
	}
	store, ok := s.cache.(AnthropicCompatChatStore)
	if !ok {
		return compatUnavailable("Chat continuation storage is unavailable")
	}
	seeds := store.HMACSeeds()
	if len(seeds) == 0 || len(seeds) > 2 {
		return compatUnavailable("Invalid Chat continuation keyring")
	}
	prefixesBySeed := make(map[string][]string, len(seeds))
	for _, seed := range seeds {
		prefixes, err := compatChatPredecessorDigests(seed, state.Messages)
		if err != nil {
			return err
		}
		prefixesBySeed[string(seed)] = prefixes
	}
	keys := make([]string, 0, len(handles)*len(seeds))
	for _, handle := range handles {
		for _, seed := range seeds {
			if len(seed) < 32 {
				return compatUnavailable("Invalid Chat continuation keyring")
			}
			keys = append(keys, compatChatKey(seed, scope, "h", handle))
		}
	}
	values, err := store.LookupChat(ctx, keys)
	if err != nil || len(values) != len(keys) {
		return compatUnavailable("Chat continuation lookup failed")
	}
	byTurn := map[string]*anthropicCompatChatRead{}
	rootBytes := 0
	for i, handle := range handles {
		var read *anthropicCompatChatRead
		for j, seed := range seeds {
			value := values[i*len(seeds)+j]
			if len(value) == 0 {
				continue
			}
			if len(value) >= 33 {
				if existing := byTurn[string(value[1:33])]; existing != nil {
					if !bytes.Equal(existing.Encrypted, value) {
						return compatChatHistoryError("Conflicting Chat assistant turn")
					}
					if _, found := existing.Turn.Handles[handle]; !found {
						return compatChatHistoryError("Chat handle does not belong to its assistant turn")
					}
					read = existing
					continue
				}
			}
			rootBytes += len(value)
			if rootBytes > 8*AnthropicCompatChatRecordLimit {
				return compatInvalid("anthropic_compat_history_limit", "Chat restored history exceeds its byte limit")
			}
			turn, err := compatChatOpen(seed, scope, value)
			if err != nil {
				return err
			}
			if _, found := turn.Handles[handle]; !found {
				return compatChatHistoryError("Chat handle does not belong to its assistant turn")
			}
			if read != nil && read.Turn.ID != turn.ID {
				return compatChatHistoryError("Conflicting Chat continuation records")
			}
			if existing := byTurn[turn.ID]; existing != nil {
				if !bytes.Equal(existing.Encrypted, value) {
					return compatChatHistoryError("Conflicting Chat assistant turn")
				}
				read = existing
			} else {
				read = &anthropicCompatChatRead{Turn: turn, Seed: append([]byte(nil), seed...), Encrypted: append([]byte(nil), value...)}
				byTurn[turn.ID] = read
				state.Reads = append(state.Reads, read)
			}
		}
		if read == nil {
			return compatInvalid("anthropic_compat_history_incompatible", "Unknown or expired gateway Chat tool call handle", AnthropicCompatReasonUnknownHistory)
		}
		state.Restores[handle] = read
	}
	used := map[string]bool{}
	outputs := map[string]bool{}
	for i, raw := range state.Messages {
		msg := gjson.ParseBytes(raw)
		calls := msg.Get("tool_calls").Array()
		var read *anthropicCompatChatRead
		for _, call := range calls {
			if current := state.Restores[call.Get("id").String()]; current != nil {
				if read != nil && read != current {
					return compatChatHistoryError("Chat assistant mixes different gateway turns")
				}
				read = current
			}
		}
		if read == nil {
			continue
		}
		if msg.Get("role").String() != "assistant" || len(calls) != len(read.Turn.Handles) {
			return compatChatHistoryError("Chat assistant tool call set changed")
		}
		for _, call := range calls {
			id := call.Get("id").String()
			if state.Restores[id] != read || used[id] {
				return compatChatHistoryError("Chat assistant tool call set changed")
			}
			used[id] = true
		}
		predecessor := prefixesBySeed[string(read.Seed)][i]
		if predecessor != read.Turn.Predecessor {
			return compatChatHistoryError("Chat continuation predecessor history changed")
		}
		visible, err := compatChatDigest(read.Seed, "visible", []json.RawMessage{raw})
		if err != nil || visible != read.Turn.Visible {
			return compatChatHistoryError("Chat assistant content, tool name or arguments changed")
		}
		pending := len(calls)
		for j := i + 1; j < len(state.Messages) && gjson.GetBytes(state.Messages[j], "role").String() == "tool"; j++ {
			output := gjson.ParseBytes(state.Messages[j])
			id := output.Get("tool_call_id").String()
			if state.Restores[id] != read || outputs[id] {
				return compatChatHistoryError("Chat tool outputs do not match the assistant turn")
			}
			if name := output.Get("name").String(); name != "" {
				for _, call := range calls {
					if call.Get("id").String() == id && call.Get("function.name").String() != name {
						return compatChatHistoryError("Chat tool output name changed")
					}
				}
			}
			outputs[id] = true
			pending--
		}
		if pending != 0 {
			return compatChatHistoryError("Chat continuation requires all parallel tool outputs exactly once")
		}
		state.Validated[i] = read
	}
	for _, handle := range handles {
		if !used[handle] || !outputs[handle] || outputRefs[handle] != 1 {
			return compatChatHistoryError("Chat tool handle has no complete assistant/tool pair")
		}
	}
	// A text-only response cannot carry a standard tool handle. It can still be
	// resolved under an authenticated random parent turn and the exact visible
	// predecessor. This lookup is never available to a prompt-only conversation.
	state.UnmanagedReasoning = false
	childLookups, restoredBytes := 0, 0
	for _, read := range state.Reads {
		restoredBytes += len(read.Encrypted)
	}
	for i, raw := range state.Messages {
		if read := state.Validated[i]; read != nil {
			state.LastTurn = read
			continue
		}
		msg := gjson.ParseBytes(raw)
		if msg.Get("role").String() != "assistant" {
			continue
		}
		if state.LastTurn == nil {
			state.UnmanagedReasoning = state.UnmanagedReasoning || compatChatMessageReasoning(msg)
			continue
		}
		childLookups++
		if childLookups > 64 {
			return compatInvalid("anthropic_compat_history_limit", "Chat continuation contains too many assistant turns")
		}
		childKeys := make([]string, 0, len(seeds))
		for _, seed := range seeds {
			predecessor := prefixesBySeed[string(seed)][i]
			visible, err := compatChatDigest(seed, "visible", []json.RawMessage{raw})
			if err != nil {
				return err
			}
			childKeys = append(childKeys, compatChatChildKey(seed, scope, state.LastTurn.Turn.ID, predecessor, visible))
		}
		childValues, err := store.LookupChat(ctx, childKeys)
		if err != nil || len(childValues) != len(childKeys) {
			return compatUnavailable("Chat assistant continuation lookup failed")
		}
		var child *anthropicCompatChatRead
		for j, value := range childValues {
			if len(value) == 0 {
				continue
			}
			turn, err := compatChatOpen(seeds[j], scope, value)
			if err != nil {
				return err
			}
			if turn.Parent != state.LastTurn.Turn.ID || len(turn.Handles) != 0 || compatChatChildKey(seeds[j], scope, turn.Parent, turn.Predecessor, turn.Visible) != childKeys[j] {
				return compatChatHistoryError("Chat child assistant constraints changed")
			}
			if child != nil && child.Turn.ID != turn.ID {
				return compatChatHistoryError("Conflicting Chat child assistant turn")
			}
			child = &anthropicCompatChatRead{Turn: turn, Seed: append([]byte(nil), seeds[j]...), Encrypted: append([]byte(nil), value...)}
		}
		if child == nil {
			return compatInvalid("anthropic_compat_history_incompatible", "Unknown, changed or expired Chat assistant continuation", AnthropicCompatReasonUnknownHistory)
		}
		restoredBytes += len(child.Encrypted)
		if restoredBytes > 8*AnthropicCompatChatRecordLimit {
			return compatInvalid("anthropic_compat_history_limit", "Chat restored history exceeds its byte limit")
		}
		state.Validated[i], state.LastTurn = child, child
		state.Reads = append(state.Reads, child)
	}
	for _, read := range state.Reads {
		if scope.Record != nil && *scope.Record != read.Turn.Constraints {
			return compatChatHistoryError("Chat history contains different account or profile constraints")
		}
		record := read.Turn.Constraints
		scope.Record = &record
		scope.Anchor = record.Anchor
		for _, block := range read.Turn.Blocks {
			kind, value := gjson.GetBytes(block, "type").String(), ""
			switch kind {
			case "thinking":
				value = gjson.GetBytes(block, "signature").String()
			case "redacted_thinking":
				value = gjson.GetBytes(block, "data").String()
			}
			if value != "" {
				scope.Signatures = append(scope.Signatures, anthropicCompatOpaque{Type: kind, Value: value})
			}
		}
	}
	return validateAnthropicCompatHistory(scope)
}

func compatChatVisibleMessage(blocks []json.RawMessage, handles map[string]string, rewrite *ToolNameRewrite) (json.RawMessage, error) {
	var content []apicompat.AnthropicContentBlock
	for _, raw := range blocks {
		var block apicompat.AnthropicContentBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		if block.Type == "tool_use" {
			for handle, id := range handles {
				if block.ID == id {
					block.ID = handle
					break
				}
			}
		}
		content = append(content, block)
	}
	response := &apicompat.AnthropicResponse{Content: content, StopReason: apicompat.AnthropicStopReasonPtr("tool_use")}
	chat := apicompat.ResponsesToChatCompletions(apicompat.AnthropicToResponsesResponse(response), "")
	raw, err := json.Marshal(chat.Choices[0].Message)
	if err != nil {
		return nil, err
	}
	return restoreToolNamesInBytes(raw, rewrite), nil
}

func normalizeAnthropicCompatChatConversion(ctx context.Context, req *apicompat.ChatCompletionsRequest) {
	scope := compatScope(ctx)
	if scope == nil || scope.ChatState == nil || len(scope.ChatState.Restores) == 0 {
		return
	}
	for i := range req.Messages {
		if scope.ChatState.Validated[i] != nil {
			// The ordered original signed blocks are restored after conversion.
			// Retained display reasoning must not become duplicate text tags.
			req.Messages[i].Reasoning = ""
			req.Messages[i].ReasoningContent = ""
		}
	}
}

func compatChatConvertedHandle(id string) string {
	if strings.HasPrefix(id, "toolu_"+AnthropicCompatChatHandlePrefix) {
		return strings.TrimPrefix(id, "toolu_")
	}
	return id
}

func restoreAnthropicCompatChatBody(ctx context.Context, body []byte) ([]byte, error) {
	scope := compatScope(ctx)
	if scope == nil || scope.ChatState == nil || len(scope.ChatState.Restores) == 0 {
		return body, nil
	}
	state := scope.ChatState
	sequence := make([]*anthropicCompatChatRead, 0, len(state.Validated))
	for index := range state.Messages {
		if read := state.Validated[index]; read != nil {
			sequence = append(sequence, read)
		}
	}
	next := 0
	var messages []json.RawMessage
	if json.Unmarshal([]byte(gjson.GetBytes(body, "messages").Raw), &messages) != nil {
		return nil, compatChatHistoryError("Invalid converted Chat messages")
	}
	for i, raw := range messages {
		var blocks []json.RawMessage
		if json.Unmarshal([]byte(gjson.GetBytes(raw, "content").Raw), &blocks) != nil {
			continue
		}
		var read *anthropicCompatChatRead
		for j, block := range blocks {
			switch gjson.GetBytes(block, "type").String() {
			case "tool_use":
				handle := compatChatConvertedHandle(gjson.GetBytes(block, "id").String())
				current := state.Restores[handle]
				if current != nil {
					if read != nil && read != current {
						return nil, compatChatHistoryError("Converted Chat assistant merges different signed turns")
					}
					read = current
					var err error
					blocks[j], err = sjson.SetBytes(block, "id", handle)
					if err != nil {
						return nil, err
					}
				}
			case "tool_result":
				id := compatChatConvertedHandle(gjson.GetBytes(block, "tool_use_id").String())
				if current := state.Restores[id]; current != nil {
					updated, err := sjson.SetBytes(block, "tool_use_id", current.Turn.Handles[id])
					if err != nil {
						return nil, err
					}
					blocks[j] = updated
				}
			}
		}
		if gjson.GetBytes(raw, "role").String() == "assistant" && next < len(sequence) {
			expected := sequence[next]
			if read != nil && read != expected {
				return nil, compatChatHistoryError("Converted Chat assistant order changed")
			}
			visible, err := compatChatVisibleMessage(blocks, expected.Turn.Handles, nil)
			if err != nil {
				return nil, err
			}
			digest, err := compatChatDigest(expected.Seed, "visible", []json.RawMessage{visible})
			if err != nil {
				return nil, err
			}
			if digest == expected.Turn.Visible {
				blocks = expected.Turn.Blocks
				next++
			} else if read != nil || next > 0 {
				return nil, compatChatHistoryError("Converted Chat assistant changed its signed turn boundary")
			}
		}
		encoded, _ := json.Marshal(blocks)
		var err error
		messages[i], err = sjson.SetRawBytes(raw, "content", encoded)
		if err != nil {
			return nil, err
		}
	}
	if next != len(sequence) {
		return nil, compatChatHistoryError("Converted Chat history lost a signed assistant turn")
	}
	encoded, _ := json.Marshal(messages)
	return sjson.SetRawBytes(body, "messages", encoded)
}

func (s *GatewayService) prepareAnthropicCompatChatResponse(c *gin.Context, plan *anthropicCompatPlan) error {
	if plan == nil || !plan.Active || plan.Profile.ID != AnthropicCompatProfileClaudeCodeV2 || plan.Scope.Entry != "chat" {
		return nil
	}
	store, ok := s.cache.(AnthropicCompatChatStore)
	if !ok || len(store.HMACSeed()) < 32 || plan.Scope.ChatState == nil {
		return compatUnavailable("Chat continuation storage is unavailable")
	}
	plan.Scope.ChatState.ResponseRewrite = toolNameRewriteFromContext(c)
	return nil
}

type anthropicCompatChatResponseState struct {
	blocks   []json.RawMessage
	partials map[int]*strings.Builder
	fields   map[int]map[string]*strings.Builder
	handles  map[string]string
	bytes    int
}

func newAnthropicCompatChatResponseState(plan *anthropicCompatPlan) *anthropicCompatChatResponseState {
	if plan == nil || plan.Profile == nil || plan.Profile.ID != AnthropicCompatProfileClaudeCodeV2 || plan.Scope == nil || plan.Scope.Entry != "chat" {
		return nil
	}
	return &anthropicCompatChatResponseState{partials: map[int]*strings.Builder{}, fields: map[int]map[string]*strings.Builder{}, handles: map[string]string{}}
}

// observe retains the exact ordered assistant blocks while rewriting IDs before
// downstream converters can expose them. Text and arguments still stream normally.
func (state *anthropicCompatChatResponseState) observe(kind string, payload []byte) ([]byte, error) {
	index := int(gjson.GetBytes(payload, "index").Int())
	switch kind {
	case "message_start":
		if len(gjson.GetBytes(payload, "message.content").Array()) != 0 {
			return nil, compatUnavailable("Chat continuation requires incremental assistant blocks")
		}
	case "content_block_start":
		if index != len(state.blocks) || len(state.blocks) >= anthropicCompatSignatureLimit {
			return nil, compatUnavailable("Invalid Chat assistant block order")
		}
		block := []byte(gjson.GetBytes(payload, "content_block").Raw)
		switch gjson.GetBytes(block, "type").String() {
		case "text", "thinking", "redacted_thinking", "tool_use":
		default:
			return nil, compatUnavailable("Chat continuation cannot preserve this assistant block")
		}
		state.bytes += len(block)
		state.blocks = append(state.blocks, append(json.RawMessage(nil), block...))
		if gjson.GetBytes(block, "type").String() == "tool_use" {
			originalID := gjson.GetBytes(block, "id").String()
			if originalID == "" {
				return nil, compatUnavailable("Chat tool call has no upstream ID")
			}
			for _, prior := range state.handles {
				if prior == originalID {
					return nil, compatUnavailable("Chat assistant repeats an upstream tool ID")
				}
			}
			random, err := compatChatRandomID()
			if err != nil {
				return nil, err
			}
			handle := AnthropicCompatChatHandlePrefix + random
			state.handles[handle] = originalID
			payload, err = sjson.SetBytes(payload, "content_block.id", handle)
			if err != nil {
				return nil, err
			}
		}
	case "content_block_delta":
		if index < 0 || index >= len(state.blocks) {
			return nil, compatUnavailable("Chat delta has no assistant block")
		}
		delta := gjson.GetBytes(payload, "delta")
		blockKind := gjson.GetBytes(state.blocks[index], "type").String()
		field, value := "", ""
		switch delta.Get("type").String() {
		case "text_delta":
			if blockKind != "text" {
				return nil, compatUnavailable("Chat text delta has incompatible assistant block")
			}
			field, value = "text", delta.Get("text").String()
		case "thinking_delta":
			if blockKind != "thinking" {
				return nil, compatUnavailable("Chat thinking delta has incompatible assistant block")
			}
			field, value = "thinking", delta.Get("thinking").String()
		case "signature_delta":
			if blockKind != "thinking" {
				return nil, compatUnavailable("Chat signature delta has incompatible assistant block")
			}
			field, value = "signature", delta.Get("signature").String()
		case "input_json_delta":
			if blockKind != "tool_use" {
				return nil, compatUnavailable("Chat tool delta has incompatible assistant block")
			}
			value = delta.Get("partial_json").String()
			if state.partials[index] == nil {
				state.partials[index] = &strings.Builder{}
			}
			_, _ = state.partials[index].WriteString(value)
		default:
			return nil, compatUnavailable("Chat continuation cannot preserve this assistant delta")
		}
		state.bytes += len(value)
		if field != "" {
			if state.fields[index] == nil {
				state.fields[index] = make(map[string]*strings.Builder)
			}
			if state.fields[index][field] == nil {
				state.fields[index][field] = &strings.Builder{}
				_, _ = state.fields[index][field].WriteString(gjson.GetBytes(state.blocks[index], field).String())
			}
			_, _ = state.fields[index][field].WriteString(value)
		}
	case "content_block_stop":
		for field, value := range state.fields[index] {
			updated, err := sjson.SetBytes(state.blocks[index], field, value.String())
			if err != nil {
				return nil, err
			}
			state.blocks[index] = updated
		}
		delete(state.fields, index)
		if partial := state.partials[index]; partial != nil {
			if !gjson.Valid(partial.String()) || !gjson.Parse(partial.String()).IsObject() {
				return nil, compatUnavailable("Chat tool arguments are incomplete")
			}
			updated, err := sjson.SetRawBytes(state.blocks[index], "input", []byte(partial.String()))
			if err != nil {
				return nil, err
			}
			state.blocks[index] = updated
			delete(state.partials, index)
		}
	}
	if state.bytes > AnthropicCompatChatRecordLimit {
		return nil, compatUnavailable("Chat assistant exceeds the continuation byte limit")
	}
	return payload, nil
}

func (s *GatewayService) commitAnthropicCompatChat(ctx context.Context, plan *anthropicCompatPlan, state *anthropicCompatChatResponseState) error {
	if state == nil {
		return nil
	}
	store, ok := s.cache.(AnthropicCompatChatStore)
	if !ok || plan.Scope.ChatState == nil {
		return compatUnavailable("Chat continuation storage is unavailable")
	}
	request := plan.Scope.ChatState
	// Refresh every immutable parent only after the new upstream response has a
	// valid successful terminal. Parallel branches never overwrite their parent.
	for _, read := range request.Reads {
		keys := compatChatTurnAliases(read.Seed, plan.Scope, read.Turn)
		if err := store.RegisterChat(ctx, compatChatKey(read.Seed, plan.Scope, "t", read.Turn.ID), keys, read.Encrypted, anthropicCompatProvenanceTTL); err != nil {
			return compatUnavailable("Chat continuation retention could not be refreshed")
		}
	}
	if len(state.handles) == 0 && request.LastTurn == nil {
		return nil
	}
	if len(state.blocks) == 0 {
		return compatUnavailable("Chat continuation has no assistant content blocks")
	}
	seed := store.HMACSeed()
	id, err := compatChatRandomID()
	if err != nil {
		return compatUnavailable("Chat continuation identity could not be generated")
	}
	predecessor, err := compatChatDigest(seed, "predecessor", request.Messages)
	if err != nil {
		return err
	}
	visible, err := compatChatVisibleMessage(state.blocks, state.handles, request.ResponseRewrite)
	if err != nil {
		return compatUnavailable("Chat assistant could not be serialized")
	}
	visibleDigest, err := compatChatDigest(seed, "visible", []json.RawMessage{visible})
	if err != nil {
		return err
	}
	turn := &anthropicCompatChatTurn{Version: 1, ID: id, Constraints: plan.Record, Predecessor: predecessor, Visible: visibleDigest, Blocks: state.blocks, Handles: state.handles}
	if len(state.handles) == 0 {
		turn.Parent = request.LastTurn.Turn.ID
	}
	encrypted, err := compatChatSeal(seed, plan.Scope, turn)
	if err != nil {
		return compatUnavailable("Chat continuation could not be encrypted")
	}
	keys := compatChatTurnAliases(seed, plan.Scope, turn)
	if len(turn.Handles) == 0 {
		if found, err := reuseAnthropicCompatChatChild(ctx, store, plan.Scope, turn, request.Messages, visible); err != nil || found {
			return err
		}
	}
	if err := store.RegisterChat(ctx, compatChatKey(seed, plan.Scope, "t", id), keys, encrypted, anthropicCompatProvenanceTTL); err != nil {
		if len(turn.Handles) == 0 {
			if found, reuseErr := reuseAnthropicCompatChatChild(ctx, store, plan.Scope, turn, request.Messages, visible); reuseErr != nil || found {
				return reuseErr
			}
		}
		return compatUnavailable("Chat continuation could not be saved")
	}
	return nil
}

func reuseAnthropicCompatChatChild(ctx context.Context, store AnthropicCompatChatStore, scope *anthropicCompatScope, turn *anthropicCompatChatTurn, messages []json.RawMessage, visible json.RawMessage) (bool, error) {
	seeds := store.HMACSeeds()
	if len(seeds) == 0 || len(seeds) > 2 {
		return false, compatUnavailable("Invalid Chat continuation keyring")
	}
	keys := make([]string, 0, len(seeds))
	wants := make([]anthropicCompatChatTurn, 0, len(seeds))
	for _, seed := range seeds {
		if len(seed) < 32 {
			return false, compatUnavailable("Invalid Chat continuation keyring")
		}
		want := *turn
		var err error
		want.Predecessor, err = compatChatDigest(seed, "predecessor", messages)
		if err != nil {
			return false, err
		}
		want.Visible, err = compatChatDigest(seed, "visible", []json.RawMessage{visible})
		if err != nil {
			return false, err
		}
		keys = append(keys, compatChatChildKey(seed, scope, want.Parent, want.Predecessor, want.Visible))
		wants = append(wants, want)
	}
	values, err := store.LookupChat(ctx, keys)
	if err != nil || len(values) != len(keys) {
		return false, compatUnavailable("Chat child continuation lookup failed")
	}
	foundID := ""
	for i, value := range values {
		if len(value) == 0 {
			continue
		}
		existing, err := compatChatOpen(seeds[i], scope, value)
		if err != nil {
			return false, err
		}
		if foundID != "" && foundID != existing.ID {
			return false, compatUnavailable("Conflicting signed Chat child assistant", AnthropicCompatReasonRegistrationConflict)
		}
		foundID = existing.ID
		// The random record ID is not assistant semantics. The key epoch's
		// authenticated digests are recomputed before comparing exact blocks.
		want := wants[i]
		want.ID = existing.ID
		a, err := json.Marshal(&want)
		if err != nil {
			return false, err
		}
		b, err := json.Marshal(existing)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(a, b) {
			return false, compatUnavailable("Conflicting signed Chat child assistant", AnthropicCompatReasonRegistrationConflict)
		}
		if err := store.RegisterChat(ctx, compatChatKey(seeds[i], scope, "t", existing.ID), []string{keys[i]}, value, anthropicCompatProvenanceTTL); err != nil {
			return false, compatUnavailable("Chat child continuation retention could not be refreshed")
		}
	}
	return foundID != "", nil
}
