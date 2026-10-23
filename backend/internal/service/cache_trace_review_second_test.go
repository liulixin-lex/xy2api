package service

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheTraceUsageStringDisabledAllocationsAndFiveSites(t *testing.T) {
	c := cacheTestContext(1, 10)
	payload := strings.Repeat("private-payload", 2000)
	require.Zero(t, testing.AllocsPerRun(1000, func() { observeCacheTraceUsageString(c, payload) }))
	// Cover the argument evaluation at all five real hooks, not just the helper.
	for _, file := range []string{"openai_gateway_chat_completions.go", "openai_gateway_chat_completions_raw.go", "openai_gateway_cc_pipeline.go"} {
		content, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(content), "observeCacheTraceUsageString(c, payload)"))
		require.NotContains(t, string(content), "observeCacheTraceUsage(c, []byte(payload))")
	}
	content, err := os.ReadFile("openai_gateway_messages.go")
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(content), "observeCacheTraceUsage(c, payloadBytes)"))
	require.Equal(t, 2, strings.Count(string(content), "parseSSEUsageBytesWithType(payloadBytes, event.Type, &usage)"))
	require.NotContains(t, string(content), "observeCacheTraceUsage(c, []byte(payload))")
}

func TestCacheTraceUsageWriteNumberPresence(t *testing.T) {
	for _, value := range []string{"null", `"0"`, "false", "{}", "[]", "0", "1024"} {
		s, c, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "none", "none", traceTestConfig)
		_ = s
		observeCacheTraceUsageString(c, `{"usage":{"cache_write_tokens":`+value+`}}`)
		expected := uint8(0)
		if value == "0" || value == "1024" {
			expected = 16
		}
		require.Equal(t, expected, cacheTraceFromGin(c).data.UsageFields&16)
	}
}

func TestCacheTraceRawCCAndPassthroughSuccess(t *testing.T) {
	payload := `{"id":"raw","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2000,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens":2}}`
	c, _, _, _, result := traceExtraRun(t, traceExtraCase{Scenario: "rawcc_apikey", Key: "none", Header: "none"}, traceTestConfig, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))})
	require.NotNil(t, result)
	require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
	require.Equal(t, uint8(1), cacheTraceFromGin(c).data.UsageFields&15)
	for _, kind := range []string{"apikey", "oauth"} {
		// Both passthrough and standard Responses accept a JSON final response.
		for _, pt := range []bool{false, true} {
			response := `{"id":"resp_json","object":"response","status":"completed","output":[],"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1024},"output_tokens":1}}`
			scenario := "responses_" + kind
			upstream := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(response))}
			// OAuth passthrough forces stream upstream, so its successful response is SSE.
			if kind == "oauth" && pt {
				upstream = traceForwardOK()
			}
			c, _, _, _, result := traceExtraRun(t, traceExtraCase{Scenario: scenario, Key: "none", Header: "none"}, traceTestConfig, upstream, func(a *Account) { a.Extra["openai_passthrough"] = pt })
			require.NotNil(t, result, "%s pt=%v", kind, pt)
			require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
			require.Equal(t, uint8(1), cacheTraceFromGin(c).data.UsageFields&15)
		}
	}
}
