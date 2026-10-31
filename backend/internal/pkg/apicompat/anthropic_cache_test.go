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
