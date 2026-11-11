package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestClaudeAPIKeyCompatSignedHistoryKeepsOrderAndMultiplicity(t *testing.T) {
	blocks := []map[string]string{
		{"type": "thinking", "signature": "first"},
		{"type": "redacted_thinking", "data": "second"},
		{"type": "thinking", "signature": "first"},
	}
	want := []anthropicCompatOpaque{{"thinking", "first"}, {"redacted_thinking", "second"}, {"thinking", "first"}}
	for _, entry := range []string{"messages", "responses"} {
		t.Run(entry, func(t *testing.T) {
			var request any = map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": blocks}}}
			if entry == "responses" {
				items := make([]any, 0, len(blocks))
				for _, block := range blocks {
					raw, err := json.Marshal(block)
					require.NoError(t, err)
					items = append(items, map[string]string{"type": "reasoning", "encrypted_content": "anthropic-thinking-v1:" + base64.RawStdEncoding.EncodeToString(raw)})
				}
				request = map[string]any{"input": items}
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			got, err := extractAnthropicCompatOpaque(body, entry)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
	// Repeated blocks consume the same limits as distinct blocks.
	body := []byte(`{"messages":[{"role":"assistant","content":[` + strings.TrimSuffix(strings.Repeat(`{"type":"thinking","signature":"same"},`, anthropicCompatSignatureLimit+1), ",") + `]}]}`)
	values, err := extractAnthropicCompatOpaque(body, "messages")
	require.NoError(t, err)
	require.ErrorContains(t, validateAnthropicCompatHistory(&anthropicCompatScope{Signatures: values}), "limits")
}

func TestClaudeAPIKeyCompatStreamSignedBlocksUseContentOrder(t *testing.T) {
	store := newCompatMemoryStore()
	s, plan, ctx := compatObserverFixture(t, store)
	stream := "data: {\"type\":\"message_start\"}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"signature\":\"first\"}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"second\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"thinking\",\"signature\":\"first\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":2}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}
	s.observeAnthropicCompatResponse(ctx, resp, plan)
	_, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	observer, ok := resp.Body.(*anthropicCompatResponseBody)
	require.True(t, ok)
	require.Equal(t, []anthropicCompatOpaque{{"thinking", "first"}, {"redacted_thinking", "second"}, {"thinking", "first"}}, observer.values)
	require.Equal(t, 1, store.registers)
	require.Len(t, store.records, 2, "only persistence keys may be deduplicated")
}

func TestClaudeAPIKeyCompatStreamRejectsAmbiguousBlockIndexes(t *testing.T) {
	for _, index := range []string{"0", "-1", "0.5", `"1"`, "null"} {
		t.Run(index, func(t *testing.T) {
			store := newCompatMemoryStore()
			s, plan, ctx := compatObserverFixture(t, store)
			extra := "data: {\"type\":\"content_block_start\",\"index\":" + index + ",\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"other\"}}\n\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":" + index + "}\n\n"
			stream := strings.Replace(compatSSE("first"), "event: message_delta", extra+"event: message_delta", 1)
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}
			s.observeAnthropicCompatResponse(ctx, resp, plan)
			out, err := io.ReadAll(resp.Body)
			require.Error(t, err)
			require.NotContains(t, string(out), "message_stop")
			require.Zero(t, store.registers)
		})
	}
}

func TestClaudeAPIKeyCompatChatContinuationIsProfileSpecificCandidateConstraint(t *testing.T) {
	// Standard Chat IDs identify tool calls but carry no signed reasoning or
	// authenticated provenance. Keep that capability requirement separate from
	// malformed history so ordinary Anthropic candidates remain eligible.
	for _, messages := range []string{
		`[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]`,
		`[{"role":"tool","tool_call_id":"call_1","content":"ok"}]`,
		`[{"role":"assistant","function_call":{"name":"lookup","arguments":"{}"}}]`,
		`[{"role":"function","name":"lookup","content":"ok"}]`,
		`[{"role":"assistant","reasoning_content":"unsigned reasoning","content":"answer"}]`,
	} {
		t.Run(messages, func(t *testing.T) {
			s := compatTestService(newCompatMemoryStore())
			body := []byte(`{"model":"claude-opus-5-5","messages":` + messages + `}`)
			ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "chat", "claimed-session")
			require.NoError(t, err)
			scope := compatScope(ctx)
			require.True(t, scope.ChatContinuation)
			require.NoError(t, scope.HistoryErr)

			compat := compatTestAccount()
			require.False(t, s.anthropicCompatCandidateEligible(ctx, compat, "claude-opus-5-5"), "compatibility candidate must be filtered before slot acquisition")

			off := *compat
			off.Extra = nil
			require.True(t, s.anthropicCompatCandidateEligible(ctx, &off, "claude-opus-5-5"), "ordinary Anthropic candidates must remain eligible")

			_, err = s.newAnthropicCompatPlan(ctx, compatTestContext(), compat, body, "claude-opus-5-5")
			require.ErrorContains(t, err, "Chat")
			require.True(t, IsAnthropicCompatCandidateError(err))
			_, err = s.beginAnthropicCompatAttempt(ctx, compatTestContext(), &off, body, "chat")
			require.NoError(t, err, "legacy requests retain their existing path")
		})
	}
}

func TestClaudeAPIKeyCompatChatIDsDoNotIdentifySignedState(t *testing.T) {
	var first *apicompat.ChatCompletionsResponse
	stopReason := "tool_use"
	for _, signature := range []string{"signed-state-a", "signed-state-b"} {
		response := &apicompat.AnthropicResponse{ID: "msg_same", Type: "message", Role: "assistant", StopReason: &stopReason, Content: []apicompat.AnthropicContentBlock{
			{Type: "thinking", Thinking: "reasoning", Signature: signature},
			{Type: "tool_use", ID: "call_same", Name: "lookup", Input: json.RawMessage(`{}`)},
		}}
		chat := apicompat.ResponsesToChatCompletions(apicompat.AnthropicToResponsesResponse(response), "claude-opus-5-5")
		require.Equal(t, "call_same", chat.Choices[0].Message.ToolCalls[0].ID)
		if first == nil {
			first = chat
		} else {
			require.Equal(t, first.Choices, chat.Choices, "distinct signed states have indistinguishable Chat continuation data")
		}
	}
}

func TestClaudeAPIKeyCompatProvenanceBindingFailuresAreTerminal(t *testing.T) {
	store := newCompatMemoryStore()
	s, plan, ctx := compatObserverFixture(t, store)
	opaque := anthropicCompatOpaque{"thinking", "signed"}
	require.NoError(t, s.registerAnthropicCompat(ctx, plan, []anthropicCompatOpaque{opaque}))
	history := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","signature":"signed"}]}]}`)
	for _, ids := range [][2]int64{{12, 22}, {11, 23}} {
		unknown, err := s.PrepareAnthropicCompatibility(ctx, ids[0], ids[1], history, "messages", plan.Scope.Anchor)
		require.NoError(t, err)
		_, err = s.beginAnthropicCompatAttempt(unknown, compatTestContext(), compatTestAccount(), history, "messages")
		require.Equal(t, AnthropicCompatReasonUnknownHistory, AnthropicCompatErrorReason(err))
		require.False(t, IsAnthropicCompatCandidateError(compatCandidateError(compatScope(unknown), err)))
	}
	known, err := s.PrepareAnthropicCompatibility(ctx, 11, 22, history, "messages", "untrusted-other-session")
	require.NoError(t, err)
	require.Equal(t, plan.Scope.Anchor, compatScope(known).Anchor)
	require.NoError(t, s.guardAnthropicCompatAccount(known, compatTestAccount()))
	for _, account := range []*Account{nil, {ID: 72}} {
		err = s.guardAnthropicCompatAccount(known, account)
		require.Equal(t, AnthropicCompatReasonAccount, AnthropicCompatErrorReason(err))
		require.False(t, IsAnthropicCompatCandidateError(err))
	}
	// A v1 record survives disabling and restoring its exact profile. No
	// revision aliases or missing binding fields are inferred during upgrades.
	off := *compatTestAccount()
	off.Extra = nil
	require.Equal(t, AnthropicCompatReasonProfile, AnthropicCompatErrorReason(s.guardAnthropicCompatAccount(known, &off)))
	require.NoError(t, s.guardAnthropicCompatAccount(known, compatTestAccount()))
	for _, field := range []string{"profile", "credential", "model", "prefix", "beta", "anchor", "account"} {
		t.Run(field, func(t *testing.T) {
			record := plan.Record
			switch field {
			case "profile":
				record.ProfileRevision = "previous-profile"
			case "credential":
				record.CredentialRevision = "previous-credential"
			case "model":
				record.Model = "claude-opus-4-8"
			case "prefix":
				record.Prefix = "different-system-or-tools"
			case "beta":
				record.Beta = "different-beta"
			case "anchor":
				record.Anchor = "forked-session"
			case "account":
				record.AccountID++
			}
			raw, e := json.Marshal(record)
			require.NoError(t, e)
			// Conflicting current/previous-root records cannot be selected by
			// iteration order, including forked and cross-account bindings.
			key := compatOpaqueKeyForSeed(store.previous, plan.Scope, opaque)
			store.records[key] = raw
			_, e = s.PrepareAnthropicCompatibility(ctx, 11, 22, history, "messages", "")
			require.Equal(t, AnthropicCompatReasonConstraints, AnthropicCompatErrorReason(e))
			require.False(t, IsAnthropicCompatCandidateError(e))
			delete(store.records, key)
			bound := *compatScope(known)
			bound.Record = &record
			changed := *plan
			changed.Scope = &bound
			_, wire, _ := compatBuild(t, s, compatTestContext(), compatTestAccount(), ctx, []byte(`{"model":"claude-opus-5-5","messages":[]}`))
			require.Error(t, finalizeAnthropicCompatPlan(&changed, wire, plan.Beta))
			require.Equal(t, AnthropicCompatReasonConstraints, AnthropicCompatErrorReason(s.registerAnthropicCompat(ctx, &changed, []anthropicCompatOpaque{{"thinking", "new"}})))
		})
	}
	delete(store.records, compatOpaqueKey(store, plan.Scope, opaque))
	expired, err := s.PrepareAnthropicCompatibility(ctx, 11, 22, history, "messages", "")
	require.NoError(t, err)
	require.Equal(t, AnthropicCompatReasonUnknownHistory, AnthropicCompatErrorReason(validateAnthropicCompatHistory(compatScope(expired))))
}

func TestClaudeAPIKeyCompatRegistrationTypedReasons(t *testing.T) {
	for _, test := range []struct {
		cause  error
		reason AnthropicCompatReason
	}{
		{AnthropicCompatRegistrationConflict, AnthropicCompatReasonRegistrationConflict},
		{AnthropicCompatRegistrationCapacity, AnthropicCompatReasonRegistrationCapacity},
		{AnthropicCompatRegistrationInvalid, AnthropicCompatReasonRegistrationInvalid},
	} {
		t.Run(string(test.reason), func(t *testing.T) {
			store := newCompatMemoryStore()
			s, plan, ctx := compatObserverFixture(t, store)
			store.registerErr = fmt.Errorf("store: %w", test.cause)
			err := s.registerAnthropicCompat(ctx, plan, []anthropicCompatOpaque{{"thinking", "signed"}})
			require.Equal(t, test.reason, AnthropicCompatErrorReason(fmt.Errorf("outer: %w", err)))
			status, code, _, ok := AnthropicCompatErrorDetails(err)
			require.True(t, ok)
			require.Equal(t, http.StatusServiceUnavailable, status)
			require.Equal(t, "anthropic_compat_state_unavailable", code)
			require.Zero(t, store.registers)
		})
	}
}
