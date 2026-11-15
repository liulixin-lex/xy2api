package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM3ForeignReasoningEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, signature string
		keep            bool
	}{
		{"v1", "ccswitch-openai-reasoning-v1:eyJ0ZXh0IjoiYSJ9", false},
		{"v1 damaged", "ccswitch-openai-reasoning-v1:broken", false},
		{"v1 whitespace", " \tccswitch-openai-reasoning-v1:data\n", false},
		{"empty", " \t", false},
		{"gAAAA", "gAAAA-ciphertext", false},
		{"provider", " provider-signature ", true},
		{"unknown version", "ccswitch-openai-reasoning-v2:data", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := []AnthropicContentBlock{
				{Type: "thinking", Thinking: "thought", Signature: tc.signature},
				{Type: "text", Text: "answer"},
				{Type: "tool_use", ID: "call_a", Name: "f", Input: json.RawMessage(`{"x":1}`)},
			}
			raw, err := json.Marshal(blocks)
			require.NoError(t, err)
			items, err := anthropicAssistantToResponses(raw)
			require.NoError(t, err)
			if tc.keep {
				require.Len(t, items, 3)
				require.Equal(t, "reasoning", items[0].Type)
				require.Equal(t, strings.TrimSpace(tc.signature), items[0].EncryptedContent)
				items = items[1:]
			}
			require.Len(t, items, 2)
			require.Equal(t, "message", items[0].Type)
			require.JSONEq(t, `[{"type":"output_text","text":"answer"}]`, string(items[0].Content))
			require.Equal(t, "function_call", items[1].Type)
			require.Equal(t, "call_a", items[1].CallID)
			require.Equal(t, `{"x":1}`, items[1].Arguments)
		})
	}
}

func TestM3TopLevelCacheControlBoundary(t *testing.T) {
	var req AnthropicRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"test","max_tokens":1024,"cache_control":{"type":"ephemeral","ttl":"1h","scope":"global","future_flag":true},"system":[{"type":"text","text":"instructions","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`), &req))
	before, err := json.Marshal(req)
	require.NoError(t, err)
	responses, err := AnthropicToResponses(&req)
	require.NoError(t, err)
	native, err := responses.NativeWireRequest()
	require.NoError(t, err)
	chat, err := AnthropicToChatCompletionsRequest(&req)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		wire any
	}{
		{"responses", responses}, {"native responses", native}, {"chat", chat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := json.Marshal(tc.wire)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(wire, &fields))
			require.NotContains(t, fields, "cache_control")
			// Existing block markers are not copied into either OpenAI wire.
			require.NotContains(t, string(wire), `"cache_control"`)
		})
	}
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	require.Equal(t, "1h", req.CacheControl.TTL)
	require.Equal(t, "global", req.CacheControl.Scope)
	require.JSONEq(t, `true`, string(req.CacheControl.Extra["future_flag"]))
}

func TestM3ToolConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, fields            string
		strict, notice, invalid bool
	}{
		{name: "default"}, {name: "true", fields: `,"strict":true`, strict: true}, {name: "false", fields: `,"strict":false`},
		{name: "null", fields: `,"allowed_callers":null`}, {name: "direct", fields: `,"allowed_callers":["direct"]`},
		{name: "executor", fields: `,"allowed_callers":["code_execution_20250825"]`, notice: true},
		{name: "mixed", fields: `,"allowed_callers":["direct","executor"]`, notice: true},
		{name: "empty", fields: `,"allowed_callers":[]`, notice: true}, {name: "empty name", fields: `,"allowed_callers":[""]`, notice: true},
		{name: "string", fields: `,"allowed_callers":"direct"`, invalid: true}, {name: "number", fields: `,"allowed_callers":[1]`, invalid: true},
		{name: "null member", fields: `,"allowed_callers":[null]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req AnthropicRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":{"type":"object","properties":{"optional":{"type":"string"}}}`+tc.fields+`}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`), &req))
			var notices []ConversionNotice
			out, err := AnthropicToResponsesWithOptions(&req, ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }})
			if tc.invalid {
				var bad *UnsupportedConversionError
				require.ErrorAs(t, err, &bad)
				require.Equal(t, "tools[0].allowed_callers", bad.Field)
				return
			}
			require.NoError(t, err)
			require.Len(t, out.Tools, 1)
			require.NotNil(t, out.Tools[0].Strict)
			require.Equal(t, tc.strict, *out.Tools[0].Strict)
			require.JSONEq(t, string(req.Tools[0].InputSchema), string(out.Tools[0].Parameters))
			require.False(t, *out.ParallelToolCalls)
			if tc.notice {
				require.Equal(t, []ConversionNotice{{Field: "tools[0].allowed_callers", Action: "omit"}}, notices)
			} else {
				require.Empty(t, notices)
			}
			legacy, err := AnthropicToResponses(&req)
			require.NoError(t, err)
			require.Equal(t, legacy, out)
			zero, err := AnthropicToResponsesWithOptions(&req, ConversionOptions{})
			require.NoError(t, err)
			require.Equal(t, legacy, zero)
		})
	}
}
