package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIUsageCanonicalPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, body, source string
		read               int
	}{
		{"input_details", `{"input_tokens_details":{"cached_tokens":800}}`, "input_tokens_details.cached_tokens", 800},
		{"prompt_details", `{"prompt_tokens_details":{"cached_tokens":800}}`, "prompt_tokens_details.cached_tokens", 800},
		{"read_input", `{"cache_read_input_tokens":800}`, "cache_read_input_tokens", 800},
		{"read", `{"cache_read_tokens":800}`, "cache_read_tokens", 800},
		{"cached", `{"cached_tokens":800}`, "cached_tokens", 800},
		{"explicit_zero", `{"input_tokens_details":{"cached_tokens":0},"cache_read_input_tokens":800}`, "input_tokens_details.cached_tokens", 0},
		{"alias_zero", `{"cache_read_input_tokens":0,"cached_tokens":800}`, "cache_read_input_tokens", 0},
		{"negative", `{"cache_read_tokens":-1,"cached_tokens":800}`, "cache_read_tokens", 0},
		{"null", `{"cache_read_tokens":null,"cached_tokens":800}`, "cached_tokens", 800},
		{"string", `{"cache_read_tokens":"800"}`, "", 0},
		{"fraction", `{"cache_read_tokens":0.5}`, "", 0},
		{"missing", `{}`, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := ParseOpenAIUsageCounts(gjson.Parse(tc.body))
			require.Equal(t, tc.read, counts.CacheReadTokens)
			require.Equal(t, tc.source, counts.CacheReadSource)
			var usage ResponsesUsage
			require.NoError(t, json.Unmarshal([]byte(tc.body), &usage))
			require.Equal(t, tc.source, usage.CacheReadSource)
			if tc.source != "" {
				require.Equal(t, tc.read, usage.InputTokensDetails.CachedTokens)
			}
		})
	}
}

func TestCacheControlsAndDiagnosticsSurviveConversion(t *testing.T) {
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-6.1-sol","messages":[],"prompt_cache_key":"client-key","safety_identifier":"client-safety","prompt_cache_retention":"24h","prompt_cache_options":{"ttl":"30m","diagnostics":{"comparison_response_id":"resp_test"}}}`), &req))
	converted, err := ChatCompletionsToResponses(&req)
	require.NoError(t, err)
	require.Equal(t, "24h", converted.PromptCacheRetention)
	require.JSONEq(t, string(req.PromptCacheOptions), string(converted.PromptCacheOptions))
	reverse, err := ResponsesToChatCompletionsRequest(converted)
	require.NoError(t, err)
	require.Equal(t, req.PromptCacheRetention, reverse.PromptCacheRetention)
	require.Equal(t, req.PromptCacheKey, reverse.PromptCacheKey)
	require.Equal(t, req.SafetyIdentifier, reverse.SafetyIdentifier)
	require.JSONEq(t, string(req.PromptCacheOptions), string(reverse.PromptCacheOptions))
	resp := &ResponsesResponse{PromptCacheDiagnostics: json.RawMessage(`{"type":"cache_miss","reason":"prefix_changed"}`)}
	chat := ResponsesToChatCompletions(resp, "gpt-6.1-sol")
	require.JSONEq(t, string(resp.PromptCacheDiagnostics), string(chat.PromptCacheDiagnostics))
}

func TestOpenAIUsageConversionExplicitZeroAndWritePrecedence(t *testing.T) {
	for _, cached := range []string{`"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":10,"cache_creation_tokens":99}`, `"cache_read_input_tokens":0,"cache_write_tokens":10`} {
		var usage ResponsesUsage
		require.NoError(t, json.Unmarshal([]byte(`{"input_tokens":2048,"output_tokens":1,`+cached+`}`), &usage))
		resp := &ResponsesResponse{Status: "completed", Usage: &usage, PromptCacheDiagnostics: json.RawMessage(`{"type":"cache_miss"}`)}
		chat := ResponsesToChatCompletions(resp, "gpt-5.4")
		encoded, err := json.Marshal(chat)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(encoded, "usage.prompt_tokens_details.cached_tokens").Exists())
		require.Zero(t, gjson.GetBytes(encoded, "usage.prompt_tokens_details.cached_tokens").Int())
		require.EqualValues(t, 10, gjson.GetBytes(encoded, "usage.prompt_tokens_details.cache_write_tokens").Int())
		require.False(t, gjson.GetBytes(encoded, "usage.prompt_tokens_details.cache_creation_tokens").Exists())
		for _, includeUsage := range []bool{false, true} {
			state := NewResponsesEventToChatState()
			state.IncludeUsage = includeUsage
			chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.completed", Response: resp}, state)
			require.NotEmpty(t, chunks)
			require.JSONEq(t, string(resp.PromptCacheDiagnostics), string(chunks[0].PromptCacheDiagnostics))
			if includeUsage {
				encoded, err = json.Marshal(chunks[len(chunks)-1])
				require.NoError(t, err)
				require.True(t, gjson.GetBytes(encoded, "usage.prompt_tokens_details.cached_tokens").Exists())
			}
		}
	}
}
