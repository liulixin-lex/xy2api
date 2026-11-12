//go:build unit

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func compatChatSSE(t *testing.T, content string, reason string) string {
	t.Helper()
	var blocks []map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &blocks))
	events := []map[string]any{{"type": "message_start", "message": map[string]any{"id": "msg_v2", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{}, "usage": map[string]int{"input_tokens": 11, "output_tokens": 0}}}}
	for i, block := range blocks {
		events = append(events, map[string]any{"type": "content_block_start", "index": i, "content_block": block}, map[string]any{"type": "content_block_stop", "index": i})
	}
	events = append(events, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]int{"output_tokens": 7}}, map[string]any{"type": "message_stop"})
	var out strings.Builder
	for _, event := range events {
		raw, err := json.Marshal(event)
		require.NoError(t, err)
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
	return out.String()
}

func compatChatAppend(t *testing.T, previous string, assistant map[string]any, stream bool, text string) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(previous), &body))
	delete(assistant, "reasoning_content")
	delete(assistant, "reasoning")
	messages := append(body["messages"].([]any), assistant)
	if calls, ok := assistant["tool_calls"].([]any); ok {
		for _, item := range calls {
			call := item.(map[string]any)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call["id"], "content": "found"})
		}
	}
	if text != "" {
		messages = append(messages, map[string]any{"role": "user", "content": text})
	}
	body["messages"], body["stream"] = messages, stream
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return string(raw)
}

func TestAnthropicCompatV2ChatToolRootedTextContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newHandlerCompatFixture(t)
			f.account.Extra[service.AnthropicCompatProfileExtraKey] = service.AnthropicCompatProfileClaudeCodeV2
			f.response = func(n uint64) string {
				if n == 1 {
					return handlerCompatSSE()
				}
				return compatChatSSE(t, `[{"type":"thinking","thinking":"final reasoning","signature":"signed-final-state"},{"type":"text","text":"completed answer"}]`, "end_turn")
			}
			request := handlerCompatRequest("chat", stream)
			for turn := 0; turn < 3; turn++ {
				recorder := httptest.NewRecorder()
				f.call("chat", request, recorder)
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				wire := <-f.wire
				if turn == 2 {
					require.Contains(t, string(wire.body), "synthetic-signed-thinking")
					require.Contains(t, string(wire.body), "signed-final-state")
					require.NotContains(t, string(wire.body), "toolu_ccv2_")
					require.NotContains(t, string(wire.body), "<thinking>")
				}
				assistant := handlerCompatChatAssistant(t, recorder.Body.String(), stream)
				request = compatChatAppend(t, request, assistant, stream, "next")
			}
			require.Len(t, f.store.chatTurns, 3)
			require.Len(t, f.usage.logs, 3)
			require.Zero(t, f.accounts.penalties)
		})
	}
}

func TestAnthropicCompatV2ChatParallelIntegrityAndIsolation(t *testing.T) {
	cases := []string{"valid", "missing_result", "duplicate_result", "unknown_handle", "changed_predecessor", "changed_text", "changed_name", "changed_arguments", "reordered_calls", "expired", "ciphertext_tampered", "cross_key", "cross_user"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newHandlerCompatFixture(t)
			f.account.Extra[service.AnthropicCompatProfileExtraKey] = service.AnthropicCompatProfileClaudeCodeV2
			f.response = func(uint64) string {
				return compatChatSSE(t, `[{"type":"thinking","thinking":"parallel reasoning","signature":"parallel-signed-state"},{"type":"text","text":"parallel answer"},{"type":"tool_use","id":"upstream_a","name":"lookup","input":{"q":"a"}},{"type":"tool_use","id":"upstream_b","name":"lookup","input":{"q":"b"}}]`, "tool_use")
			}
			first := handlerCompatRequest("chat", false)
			recorder := httptest.NewRecorder()
			f.call("chat", first, recorder)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			<-f.wire
			assistant := handlerCompatChatAssistant(t, recorder.Body.String(), false)
			calls := assistant["tool_calls"].([]any)
			require.Len(t, calls, 2)
			require.Len(t, f.store.chatTurns, 1)
			require.Len(t, f.store.chatAliases, 2)
			request := compatChatAppend(t, first, assistant, false, "")
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(request), &body))
			messages := body["messages"].([]any)
			returned := messages[2].(map[string]any)
			returnedCalls := returned["tool_calls"].([]any)
			switch name {
			case "missing_result":
				messages = messages[:len(messages)-1]
			case "duplicate_result":
				messages = append(messages, messages[len(messages)-1])
			case "unknown_handle":
				unknown := service.AnthropicCompatChatHandlePrefix + strings.Repeat("A", 32)
				returnedCalls[0].(map[string]any)["id"] = unknown
				messages[3].(map[string]any)["tool_call_id"] = unknown
			case "changed_predecessor":
				messages[1].(map[string]any)["content"] = "different user prompt"
			case "changed_text":
				returned["content"] = "changed answer"
			case "changed_name":
				returnedCalls[0].(map[string]any)["function"].(map[string]any)["name"] = "other"
			case "changed_arguments":
				returnedCalls[0].(map[string]any)["function"].(map[string]any)["arguments"] = `{"q":"tampered"}`
			case "reordered_calls":
				returnedCalls[0], returnedCalls[1] = returnedCalls[1], returnedCalls[0]
			case "expired":
				f.store.chatTurns = make(map[string][]byte)
			case "ciphertext_tampered":
				for key, value := range f.store.chatTurns {
					value[len(value)-1] ^= 1
					f.store.chatTurns[key] = value
				}
			case "cross_key":
				f.key.ID++
			case "cross_user":
				f.key.UserID++
				f.key.User.ID++
			}
			body["messages"] = messages
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			second := httptest.NewRecorder()
			f.call("chat", string(raw), second)
			if name == "valid" {
				require.Equal(t, http.StatusOK, second.Code, second.Body.String())
				wire := <-f.wire
				require.Contains(t, string(wire.body), "parallel-signed-state")
				require.Contains(t, string(wire.body), `"tool_use_id":"upstream_a"`)
				require.Contains(t, string(wire.body), `"tool_use_id":"upstream_b"`)
			} else {
				require.Equal(t, http.StatusBadRequest, second.Code, second.Body.String())
				require.Empty(t, f.wire, "invalid history must fail before upstream I/O")
				require.Len(t, f.usage.logs, 1)
			}
			require.Zero(t, f.accounts.penalties)
		})
	}
}

func TestAnthropicCompatV2ChatStoreFailureWithholdsTerminal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newHandlerCompatFixture(t)
			f.account.Extra[service.AnthropicCompatProfileExtraKey] = service.AnthropicCompatProfileClaudeCodeV2
			f.store.chatRegisterErr = errors.New("synthetic chat persistence failure")
			recorder := httptest.NewRecorder()
			f.call("chat", handlerCompatRequest("chat", stream), recorder)
			require.Contains(t, recorder.Body.String(), "Chat continuation could not be saved")
			require.NotContains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
			require.Empty(t, f.store.chatTurns)
			require.Len(t, f.wire, 1)
			require.Len(t, f.usage.logs, 1)
			require.Zero(t, f.accounts.penalties)
		})
	}
}

func TestAnthropicCompatV2UnsignedChatToolsRemainUsable(t *testing.T) {
	f := newHandlerCompatFixture(t)
	f.account.Extra[service.AnthropicCompatProfileExtraKey] = service.AnthropicCompatProfileClaudeCodeV2
	request := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"ordinary_call","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"ordinary_call","content":"found"}]}`
	recorder := httptest.NewRecorder()
	f.call("chat", request, recorder)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	wire := <-f.wire
	require.Equal(t, "toolu_ordinary_call", gjson.GetBytes(wire.body, "messages.1.content.0.id").String())
}
