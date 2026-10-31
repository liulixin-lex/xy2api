package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicCacheDeclarationsConversion(t *testing.T) {
	raw := []byte(`{"model":"claude-sonnet-4-5","cache_control":{"type":"ephemeral","ttl":"1h","future_flag":true},"tools":[{"type":"function","function":{"name":"probe","parameters":{"type":"object","properties":{"cache_control":{"const":"user data"}}},"cache_control":{"type":"ephemeral","ttl":"5m"},"custom":{"defer_loading":true}}}],"messages":[{"role":"system","cache_control":{"type":"ephemeral","ttl":"1h"},"content":"instructions"},{"role":"assistant","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}},{"type":"text","text":"two","cache_control":{"type":"ephemeral","ttl":"5m"}}]},{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
	var chat ChatCompletionsRequest
	require.NoError(t, json.Unmarshal(raw, &chat))
	responses, err := ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	anthropic, err := ResponsesToAnthropicRequest(responses)
	require.NoError(t, err)
	wire, err := json.Marshal(anthropic)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(wire, "cache_control.future_flag").Bool())
	require.Equal(t, "1h", gjson.GetBytes(wire, "system.0.cache_control.ttl").String())
	require.Equal(t, "global", gjson.GetBytes(wire, "messages.0.content.0.cache_control.scope").String())
	require.Equal(t, "1h", gjson.GetBytes(wire, "messages.0.content.0.cache_control.ttl").String())
	require.Equal(t, "5m", gjson.GetBytes(wire, "messages.0.content.1.cache_control.ttl").String())
	require.True(t, gjson.GetBytes(wire, "tools.0.custom.defer_loading").Bool())
	require.Equal(t, "user data", gjson.GetBytes(wire, "tools.0.input_schema.properties.cache_control.const").String())
}

func TestAnthropicUsagePresenceCumulativeConversion(t *testing.T) {
	var aggregate AnthropicUsage
	merge := func(raw string) {
		var v AnthropicUsage
		require.NoError(t, json.Unmarshal([]byte(raw), &v))
		MergeAnthropicCacheUsage(&aggregate, v)
	}
	merge(`{"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}}`)
	require.Equal(t, 50, aggregate.CacheCreationInputTokens)
	require.False(t, aggregate.CacheCreationInputTokensPresent)
	merge(`{"cache_creation":{"ephemeral_5m_input_tokens":0}}`)
	require.Equal(t, 20, aggregate.CacheCreationInputTokens)
	require.False(t, aggregate.CacheCreationInputTokensPresent)
	merge(`{"cache_creation_input_tokens":0}`)
	require.Zero(t, aggregate.CacheCreationInputTokens)
	require.True(t, aggregate.CacheCreationInputTokensPresent)
	merge(`{"output_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":25}}`)
	require.Zero(t, aggregate.CacheCreationInputTokens)
	require.Equal(t, 25, *aggregate.CacheCreation.Ephemeral1hInputTokens)
	var response AnthropicResponse
	require.NoError(t, json.Unmarshal([]byte(`{"usage":{"input_tokens":2,"output_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}}}`), &response))
	converted := AnthropicToResponsesResponse(&response)
	require.Equal(t, 52, converted.Usage.InputTokens)
	require.False(t, response.Usage.CacheCreationInputTokensPresent, "conversion must leave raw source presence untouched")
}

const testCacheImage = "data:image/png;base64,iVBORw0KGgo="

// countCacheControl counts cache_control keys at any depth of a JSON body.
func countCacheControl(wire []byte) int {
	var walk func(gjson.Result) int
	walk = func(v gjson.Result) int {
		n := 0
		v.ForEach(func(key, child gjson.Result) bool {
			if key.String() == "cache_control" {
				n++
			}
			if child.IsObject() || child.IsArray() {
				n += walk(child)
			}
			return true
		})
		return n
	}
	return walk(gjson.ParseBytes(wire))
}

func anthropicCacheWire(t *testing.T, raw string) []byte {
	t.Helper()
	var responses *ResponsesRequest
	if gjson.Get(raw, "messages").Exists() {
		var chat ChatCompletionsRequest
		require.NoError(t, json.Unmarshal([]byte(raw), &chat))
		var err error
		responses, err = ChatCompletionsToResponses(&chat)
		require.NoError(t, err)
	} else {
		responses = &ResponsesRequest{}
		require.NoError(t, json.Unmarshal([]byte(raw), responses))
	}
	anthropic, err := ResponsesToAnthropicRequest(responses)
	require.NoError(t, err)
	assertAnthropicPairing(t, anthropic.Messages)
	wire, err := json.Marshal(anthropic)
	require.NoError(t, err)
	return wire
}

func TestAnthropicCacheToolResultMarkers(t *testing.T) {
	const call = `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]}`
	const h1 = `{"type":"ephemeral","ttl":"1h"}`
	chat := func(messages string) string {
		return `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"go"},` + messages + `]}`
	}
	responses := func(input string) string {
		return `{"model":"claude-sonnet-4-5","input":[` + input + `]}`
	}
	cases := []struct {
		name, raw, path, ttl string
		check                func(t *testing.T, wire []byte)
	}{
		{name: "chat_tool_message", raw: chat(call + `,{"role":"tool","tool_call_id":"call_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h","future_flag":true}}`), path: "messages.2.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "tool_result", gjson.GetBytes(wire, "messages.2.content.0.type").String())
			require.True(t, gjson.GetBytes(wire, "messages.2.content.0.cache_control.future_flag").Bool())
		}},
		{name: "chat_tool_last_part", raw: chat(call + `,{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":` + h1 + `}]}`), path: "messages.2.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "ab", gjson.GetBytes(wire, "messages.2.content.0.content").String())
		}},
		{name: "chat_tool_outer_wins", raw: chat(call + `,{"role":"tool","tool_call_id":"call_1","cache_control":{"type":"ephemeral","ttl":"5m"},"content":[{"type":"text","text":"a","cache_control":` + h1 + `}]}`), path: "messages.2.content.0", ttl: "5m"},
		{name: "chat_tool_media_part", raw: chat(call + `,{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"w=1"},{"type":"image_url","image_url":{"url":"` + testCacheImage + `"},"cache_control":` + h1 + `}]}`), path: "messages.2.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "w=1", gjson.GetBytes(wire, "messages.2.content.0.content").String())
		}},
		{name: "chat_legacy_function", raw: chat(`{"role":"assistant","function_call":{"name":"lookup","arguments":"{}"}},{"role":"function","name":"lookup","content":"x","cache_control":` + h1 + `}`), path: "messages.2.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "call_legacy_1", gjson.GetBytes(wire, "messages.2.content.0.tool_use_id").String())
		}},
		{name: "chat_assistant_tool_calls_only", raw: chat(`{"role":"assistant","cache_control":` + h1 + `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"one"},{"role":"tool","tool_call_id":"call_2","content":"two"}`), path: "messages.1.content.1", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "call_2", gjson.GetBytes(wire, "messages.1.content.1.id").String())
			require.False(t, gjson.GetBytes(wire, "messages.1.content.0.cache_control").Exists())
		}},
		{name: "chat_assistant_text_and_tool_calls", raw: chat(`{"role":"assistant","content":"checking","cache_control":` + h1 + `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"OK"}`), path: "messages.1.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "text", gjson.GetBytes(wire, "messages.1.content.0.type").String())
			require.Equal(t, "tool_use", gjson.GetBytes(wire, "messages.1.content.1.type").String())
			require.False(t, gjson.GetBytes(wire, "messages.1.content.1.cache_control").Exists())
		}},
		{name: "chat_tool_empty_content", raw: chat(call + `,{"role":"tool","tool_call_id":"call_1","content":"","cache_control":` + h1 + `}`), path: "messages.2.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "(empty)", gjson.GetBytes(wire, "messages.2.content.0.content").String())
		}},
		{name: "responses_item", raw: responses(`{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"OK","cache_control":` + h1 + `}`), path: "messages.1.content.0", ttl: "1h"},
		{name: "responses_last_part_media", raw: responses(`{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"` + testCacheImage + `","cache_control":` + h1 + `}]}`), path: "messages.1.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "image", gjson.GetBytes(wire, "messages.1.content.0.content.1.type").String())
			require.False(t, gjson.GetBytes(wire, "messages.1.content.0.content.0.cache_control").Exists())
			require.False(t, gjson.GetBytes(wire, "messages.1.content.0.content.1.cache_control").Exists())
		}},
		{name: "responses_outer_wins", raw: responses(`{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","cache_control":{"type":"ephemeral"},"output":[{"type":"input_text","text":"a","cache_control":` + h1 + `}]}`), path: "messages.1.content.0", ttl: "", check: func(t *testing.T, wire []byte) {
			require.False(t, gjson.GetBytes(wire, "messages.1.content.0.content.0.cache_control").Exists())
		}},
		{name: "responses_function_call", raw: responses(`{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}","cache_control":` + h1 + `},{"type":"function_call_output","call_id":"call_1","output":"OK"}`), path: "messages.0.content.0", ttl: "1h", check: func(t *testing.T, wire []byte) {
			require.Equal(t, "tool_use", gjson.GetBytes(wire, "messages.0.content.0.type").String())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := anthropicCacheWire(t, tc.raw)
			cc := gjson.GetBytes(wire, tc.path+".cache_control")
			require.Equal(t, "ephemeral", cc.Get("type").String(), string(wire))
			if tc.ttl == "" {
				require.False(t, cc.Get("ttl").Exists(), string(wire))
			} else {
				require.Equal(t, tc.ttl, cc.Get("ttl").String(), string(wire))
			}
			require.Equal(t, 1, countCacheControl(wire), string(wire))
			if tc.check != nil {
				tc.check(t, wire)
			}
		})
	}
}

// Results return to call order; each marker stays on its own tool_result.
func TestAnthropicCacheToolResultParallelOrder(t *testing.T) {
	for name, raw := range map[string]string{
		"chat":      `{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_2","content":"two"},{"role":"tool","tool_call_id":"call_1","content":"one","cache_control":{"type":"ephemeral","ttl":"1h"}},{"role":"user","content":"next"}]}`,
		"responses": `{"model":"claude-sonnet-4-5","input":[{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"},{"type":"function_call","call_id":"call_2","name":"probe","arguments":"{}"},{"type":"function_call_output","call_id":"call_2","output":"two"},{"type":"function_call_output","call_id":"call_1","output":"one","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"message","role":"user","content":"next"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			wire := anthropicCacheWire(t, raw)
			require.Len(t, gjson.GetBytes(wire, "messages").Array(), 2, string(wire))
			require.Equal(t, "assistant", gjson.GetBytes(wire, "messages.0.role").String())
			content := gjson.GetBytes(wire, "messages.1.content")
			require.Len(t, content.Array(), 3, string(wire))
			require.Equal(t, "call_1", content.Get("0.tool_use_id").String())
			require.Equal(t, "1h", content.Get("0.cache_control.ttl").String())
			require.Equal(t, "call_2", content.Get("1.tool_use_id").String())
			require.False(t, content.Get("1.cache_control").Exists())
			require.Equal(t, "next", content.Get("2.text").String())
			require.Equal(t, 1, countCacheControl(wire), string(wire))
		})
	}
}

// Unmarked tool rounds stay unmarked, and Chat tool markers stay off the
// intermediate Responses input, which also feeds OpenAI Responses upstreams.
func TestAnthropicCacheToolResultMarkersAbsent(t *testing.T) {
	for _, raw := range []string{
		`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"OK"}]}]}`,
		`{"model":"claude-sonnet-4-5","input":[{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"OK"}]}]}`,
	} {
		require.NotContains(t, string(anthropicCacheWire(t, raw)), "cache_control")
	}

	raw := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"go"},{"role":"assistant","cache_control":{"type":"ephemeral"},"tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}},{"role":"function","name":"probe","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}]}`
	var chat ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &chat))
	responses, err := ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	responsesWire, err := json.Marshal(responses)
	require.NoError(t, err)
	require.NotContains(t, string(responsesWire), "cache_control", "Chat tool markers must not reach OpenAI Responses wires")
	anthropic, err := ResponsesToAnthropicRequest(responses)
	require.NoError(t, err)
	wire, err := json.Marshal(anthropic)
	require.NoError(t, err)
	require.Equal(t, "tool_use", gjson.GetBytes(wire, "messages.1.content.0.type").String())
	require.Equal(t, "ephemeral", gjson.GetBytes(wire, "messages.1.content.0.cache_control.type").String())
	require.Equal(t, "1h", gjson.GetBytes(wire, "messages.2.content.0.cache_control.ttl").String())
}
