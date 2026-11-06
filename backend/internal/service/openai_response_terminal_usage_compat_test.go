//go:build unit

package service

import (
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEffectiveOpenAISSEEventTypeKeepsPayloadTerminalAuthoritative(t *testing.T) {
	t.Parallel()

	require.Equal(t, "response.failed", effectiveOpenAISSEEventType([]byte(`{"type":"response.failed"}`), "error"))
	require.Equal(t, "response.failed", effectiveOpenAISSEEventType([]byte(`{"type":"response.failed"}`), ""))
	require.Equal(t, "error", effectiveOpenAISSEEventType([]byte(`{"error":{"message":"failed"}}`), " error "))
	require.JSONEq(t, `{"type":"error","error":{"message":"failed"}}`, openAICompatPayloadWithEventType(`{"type":"","error":{"message":"failed"}}`, "error"))
}

func TestExtractOpenAISSETerminalEventUsesEventField(t *testing.T) {
	t.Parallel()

	body := "event: error\n" +
		"data: {\"error\":{\"message\":\"provider failed\"}}\n\n" +
		"data: [DONE]\n\n"
	eventType, _, payload, ok := extractOpenAISSETerminalEvent(body)
	require.True(t, ok)
	require.Equal(t, "error", eventType)
	require.Equal(t, "provider failed", extractOpenAISSEErrorMessage(payload))
}

func TestExtractOpenAISSETerminalEventUsesFinalAuthoritativeTerminal(t *testing.T) {
	t.Parallel()

	body := "data: {\"type\":\"error\",\"error\":{\"message\":\"recovering\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"
	eventType, _, payload, ok := extractOpenAISSETerminalEvent(body)
	require.True(t, ok)
	require.Equal(t, "response.completed", eventType)
	require.Equal(t, "resp_1", gjson.GetBytes(payload, "response.id").String())
}

func TestExtractOpenAISSETerminalEventPreservesRawEventForWrapperValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, event, payload, terminal string }{
		{"E1", "error", `{"type":"proxy.upstream_error","status":400}`, "error"},
		{"E2 error event", "error", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"503","status_code":503}}}`, "response.failed"},
		{"completed error event", "error", `{"type":"response.completed","response":{"output":[]}}`, "response.completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal, sseEvent, payload, ok := extractOpenAISSETerminalEvent("event: " + tc.event + "\ndata: " + tc.payload + "\n\n")
			require.True(t, ok)
			require.Equal(t, tc.terminal, terminal)
			require.Equal(t, tc.event, sseEvent)
			require.Equal(t, tc.payload, string(payload))
		})
	}
}

func TestParseSSEUsageEffectiveTerminalRules(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}
	svc.parseSSEUsageBytesWithType([]byte(`{"usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}`), "response.in_progress", usage)
	svc.parseSSEUsageBytesWithType([]byte(`{"response":{"id":"resp_1"}}`), "response.completed", usage)
	require.Equal(t, OpenAIUsage{InputTokens: 17, OutputTokens: 5, CacheReadInputTokens: 3, CacheReadSource: "input_tokens_details.cached_tokens"}, *usage)

	svc.parseSSEUsageBytesWithType([]byte(`{"response":{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`), "response.completed", usage)
	require.Equal(t, OpenAIUsage{InputTokens: 17, OutputTokens: 5, CacheReadInputTokens: 3, CacheReadSource: "input_tokens_details.cached_tokens"}, *usage)

	svc.parseSSEUsageBytesWithType([]byte(`{"response":{"usage":{"input_tokens":2,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`), "response.completed", usage)
	require.Equal(t, OpenAIUsage{InputTokens: 2, CacheReadSource: "input_tokens_details.cached_tokens"}, *usage)
}

func BenchmarkParseSSEUsageNoUsageDelta(b *testing.B) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}
	payload := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		svc.parseSSEUsageBytesWithType(payload, "response.output_text.delta", usage)
	}
}

func TestOpenAICompatTerminalResponseSynthesizesBareError(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"error","code":"upstream_error","error":{"message":"provider failed"}}`)
	event := &apicompat.ResponsesStreamEvent{Type: "error", Code: "upstream_error"}
	response := openAICompatTerminalResponse(event, payload)
	require.NotNil(t, response)
	require.Equal(t, "failed", response.Status)
	require.Equal(t, "upstream_error", response.Error.Code)
	require.Equal(t, "provider failed", response.Error.Message)
}

func TestForEachOpenAISSEFrameDataTypeOverridesEventField(t *testing.T) {
	t.Parallel()

	var types []string
	forEachOpenAISSEFrame(strings.Join([]string{
		"event: response.in_progress",
		`data: {"type":"response.completed","response":{"id":"resp_1"}}`,
		"",
	}, "\n"), func(eventType string, _ []byte) {
		types = append(types, eventType)
	})
	require.Equal(t, []string{"response.completed"}, types)
}

func TestSampleOpenAIMissingUsageLogRateLimitsAndCounts(t *testing.T) {
	var sampler openAIMissingUsageLogSampler
	now := time.Unix(100, 0)
	logNow, total, suppressed := sampler.sample(now)
	require.True(t, logNow)
	require.Equal(t, uint64(1), total)
	require.Zero(t, suppressed)

	logNow, total, _ = sampler.sample(now.Add(time.Second))
	require.False(t, logNow)
	require.Equal(t, uint64(2), total)

	logNow, total, suppressed = sampler.sample(now.Add(openAIMissingUsageLogInterval))
	require.True(t, logNow)
	require.Equal(t, uint64(3), total)
	require.Equal(t, uint64(1), suppressed)
}
