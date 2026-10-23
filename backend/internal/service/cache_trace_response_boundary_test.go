package service

import (
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestCacheTraceResponseMetadataIndependentOfUsageSlot(t *testing.T) {
	for _, tc := range []struct {
		name, body, reasoning, pcd, ttl string
	}{
		{"response_without_usage", `{"response":{"reasoning":{"context":"current_turn"},"prompt_cache_diagnostics":["tools_changed","model_changed"],"prompt_cache_options":{"ttl":"12h"}}}`, "current_turn", "model_changed", "12h"},
		{"reasoning_only", `{"response":{"reasoning":{"context":"all_turns"}}}`, "all_turns", "absent", "absent"},
		{"ttl_only", `{"response":{"prompt_cache_options":{"ttl":"30m"}}}`, "unknown", "absent", "30m"},
		{"retention_only", `{"response":{"prompt_cache_retention":"24h"}}`, "unknown", "absent", "24h"},
		{"pcd_only", `{"response":{"prompt_cache_diagnostics":"input_changed"}}`, "unknown", "input_changed", "absent"},
		{"root_without_usage", `{"reasoning":{"context":"current_turn"},"prompt_cache_diagnostics":"tools_changed","prompt_cache_retention":"in_memory"}`, "current_turn", "tools_changed", "in_memory"},
		{"nested_data_without_usage", `{"data":{"response":{"prompt_cache_diagnostics":"model_changed","prompt_cache_options":{"ttl":"12h"},"reasoning":{"context":"all_turns"}}}}`, "all_turns", "model_changed", "12h"},
		{"usage_root_metadata_response", `{"usage":{"input_tokens":2048,"cached_tokens":1024},"response":{"reasoning":{"context":"all_turns"},"prompt_cache_options":{"ttl":"12h"},"prompt_cache_diagnostics":"tools_changed"}}`, "all_turns", "tools_changed", "12h"},
		{"per_field_envelope_fallback", `{"prompt_cache_retention":"24h","usage":{"input_tokens":2048},"response":{"reasoning":{"context":"all_turns"}},"data":{"prompt_cache_options":{"ttl":"12h"},"response":{"prompt_cache_diagnostics":"context_compacted"}}}`, "all_turns", "context_compacted", "12h"},
		{"duplicate_parent_first_complete", `{"response":{"reasoning":{},"prompt_cache_options":{}},"response":{"reasoning":{"context":"current_turn","context":"all_turns"},"prompt_cache_options":{"ttl":"12h","ttl":"24h"},"prompt_cache_diagnostics":"tools_changed","prompt_cache_diagnostics":"model_changed"}}`, "current_turn", "tools_changed", "12h"},
		{"explicit_null_does_not_fall_through", `{"reasoning":{"context":null},"prompt_cache_options":{"ttl":null},"prompt_cache_diagnostics":null,"response":{"reasoning":{"context":"all_turns"},"prompt_cache_options":{"ttl":"12h"},"prompt_cache_retention":"24h","prompt_cache_diagnostics":"model_changed"}}`, "unknown", "other", "other"},
		{"private_metadata_is_normalized", `{"response":{"reasoning":{"context":"boundary-private-context"},"prompt_cache_diagnostics":"boundary-private-pcd","prompt_cache_retention":"boundary-private-ttl"}}`, "unknown", "other", "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			want, wantOK := extractOpenAIUsageFromJSONBytes(body)
			got, observation, gotOK := extractOpenAIUsageIndexed(body)
			require.Equal(t, wantOK, gotOK)
			require.Equal(t, want, got, "metadata must not change billing extraction")
			require.Equal(t, tc.reasoning, observation.reasoning)
			require.Equal(t, tc.pcd, observation.diagnostics)
			require.Equal(t, tc.ttl, observation.ttl)
			baseline, indexed := OpenAIUsage{InputTokens: 8192, CacheReadInputTokens: 2048}, OpenAIUsage{InputTokens: 8192, CacheReadInputTokens: 2048}
			svc := &OpenAIGatewayService{}
			svc.parseSSEUsageBytesWithType(body, "response.completed", &baseline)
			observed, fold, ok := svc.parseSSEUsageBytesWithTypeIndexed(body, "response.completed", &indexed)
			require.True(t, ok, "metadata-only frames remain observable")
			require.Equal(t, baseline, indexed, "metadata must not change terminal/progressive billing fold")
			trace := &cacheTrace{data: cacheTraceData{ReasoningContextEffective: "unknown", PromptCacheDiagnostics: "absent", ResponseCacheTTL: "absent"}}
			trace.observeCacheTraceParsedResponse(observed, fold)
			require.Equal(t, tc.reasoning, trace.data.ReasoningContextEffective)
			require.Equal(t, tc.pcd, trace.data.PromptCacheDiagnostics)
			require.Equal(t, tc.ttl, trace.data.ResponseCacheTTL)
			for _, value := range []string{trace.data.ReasoningContextEffective, trace.data.PromptCacheDiagnostics, trace.data.ResponseCacheTTL} {
				require.NotContains(t, value, "boundary-private-")
			}
		})
	}
}

func TestCacheTraceResponseExplicitUnknownReasoningClearsPriorValue(t *testing.T) {
	trace := &cacheTrace{data: cacheTraceData{ReasoningContextEffective: "unknown"}}
	svc := &OpenAIGatewayService{}
	var usage OpenAIUsage
	for _, step := range []struct{ body, want string }{
		{`{"response":{"reasoning":{"context":"current_turn"}}}`, "current_turn"},
		{`{"response":{"prompt_cache_retention":"12h"}}`, "current_turn"},
		{`{"response":{"reasoning":{"context":null}}}`, "unknown"},
		{`{"response":{"reasoning":{"context":"all_turns"}}}`, "all_turns"},
		{`{"response":{"reasoning":{"context":"boundary-private-invalid"}}}`, "unknown"},
	} {
		observation, fold, ok := svc.parseSSEUsageBytesWithTypeIndexed([]byte(step.body), "response.in_progress", &usage)
		require.True(t, ok)
		trace.observeCacheTraceParsedResponse(observation, fold)
		require.Equal(t, step.want, trace.data.ReasoningContextEffective)
	}
}

func TestCacheTraceResponseMetadataHintSkipsPlainDeltas(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, body := range []string{
		`{"type":"response.output_text.delta","delta":"plain output"}`,
		`{"type":"response.output_text.delta","delta":"escaped \"reasoning\" and \"prompt_cache_options\" text"}`,
		`{"type":"response.output_text.delta","delta":"half frame`,
	} {
		data := []byte(body)
		require.False(t, cacheTraceResponseMetadataHint(data))
		var usage OpenAIUsage
		allocations := testing.AllocsPerRun(100, func() {
			observation, fold, ok := svc.parseSSEUsageBytesWithTypeIndexed(data, "response.output_text.delta", &usage)
			if ok || fold != cacheTraceUsageKeep || observation != (cacheTraceResponseObservation{}) {
				panic("plain delta unexpectedly entered indexed metadata parsing")
			}
		})
		require.Zero(t, allocations)
		require.Equal(t, OpenAIUsage{}, usage)
	}
}

func TestCacheTraceMetadataDoesNotBypassEscapedUsageBillingGuard(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, body := range []string{
		`{"us\u0061ge":{"input_tokens":2048,"cached_tokens":1024},"response":{"reasoning":{"context":"all_turns"}}}`,
		`{"us\u0061ge":{"input_tokens":2048,"cached_tokens":1024},"response":{"prompt_cache_options":{"ttl":"12h"}}}`,
	} {
		var baseline, indexed OpenAIUsage
		svc.parseSSEUsageBytesWithType([]byte(body), "response.completed", &baseline)
		_, _, observed := svc.parseSSEUsageBytesWithTypeIndexed([]byte(body), "response.completed", &indexed)
		require.True(t, observed)
		require.Equal(t, baseline, indexed)
		require.Zero(t, indexed.InputTokens)
		require.Zero(t, indexed.CacheReadInputTokens)
	}
}

func TestCacheTraceBufferedTypedReadAndTTLFieldsFollowSelectedUsage(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		read         int
		bits         uint8
		prompt       bool
	}{
		{"casefold_root_zero", `"response":{},"Usage":{"input_tokens":2048,"cached_tokens":0}`, 0, 1, true},
		{"casefold_response_read", `"Response":{"Usage":{"input_tokens":2048,"cached_tokens":1024}}`, 1024, 1, true},
		{"escaped_usage", `"response":{},"us\u0061ge":{"input_tokens":2048,"cached_tokens":1024}`, 1024, 1, true},
		{"duplicate_usage_last_read", `"response":{"usage":{"input_tokens":2048},"usage":{"input_tokens":2048,"cached_tokens":1024}}`, 1024, 1, true},
		{"duplicate_root_last_zero_ttl", `"response":{},"usage":{"input_tokens":2048},"usage":{"input_tokens":2048,"cached_tokens":0,"cache_write_tokens_details":{"ttl_30m":0,"ttl_12h":0}}`, 0, 193, true},
		{"duplicate_response_last_read_ttl", `"response":{"usage":{"input_tokens":2048}},"response":{"usage":{"input_tokens":2048,"cached_tokens":1024,"cache_write_tokens_details":{"ttl_30m":0,"ttl_12h":0}}}`, 1024, 193, true},
		{"casefold_nested_detail_zero", `"response":{"usage":{"input_tokens":2048,"INPUT_TOKENS_DETAILS":{"CACHED_TOKENS":0}}}`, 0, 1, true},
		{"casefold_nested_detail_read", `"response":{"usage":{"input_tokens":2048,"INPUT_TOKENS_DETAILS":{"CACHED_TOKENS":1024}}}`, 1024, 1, true},
		{"casefold_prompt_detail_read", `"response":{"usage":{"input_tokens":2048,"PROMPT_TOKENS_DETAILS":{"CACHED_TOKENS":1024}}}`, 1024, 1, true},
		{"typed_root_not_selected", `"Usage":{"input_tokens":2048,"cached_tokens":1024,"cache_write_tokens_details":{"ttl_30m":0}},"response":{"usage":{"input_tokens":2048}}`, 0, 0, true},
		{"null_response_resets_typed_then_empty", `"Response":{"Usage":{"input_tokens":2048,"cached_tokens":1024}},"Response":null,"response":{"usage":{"input_tokens":2048}}`, 0, 0, true},
		{"null_usage_replaced", `"response":{"usage":null,"usage":{"input_tokens":2048,"cached_tokens":0,"cache_write_tokens_details":{"ttl_12h":0}}}`, 0, 129, true},
		{"typed_usage_null_no_override", `"Response":{"Usage":{"input_tokens":2048,"cached_tokens":1024}},"Response":null,"response":{}`, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := "data: {\"type\":\"response.completed\"," + tc.fields + "}\n\n"
			var baseline OpenAIUsage
			for _, enabled := range []bool{false, true} {
				configure := traceTestConfig
				if !enabled {
					configure = func(s *OpenAIGatewayService) {
						traceTestConfig(s)
						traceTestSetPolicy(s, map[string]any{"enabled": false})
					}
				}
				svc, c, _, _ := cacheTraceWireFixture(t, "chat", "apikey", "boundary-private-pck", "session_id", configure)
				if enabled {
					resetCacheTraceAttempt(c)
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
				_, usage, _, err := svc.readOpenAICompatBufferedTerminal(resp, c, &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "gpt-5.4", "trace boundary fixture", "fixture")
				require.NoError(t, err)
				require.Equal(t, tc.read, usage.CacheReadInputTokens)
				if !enabled {
					baseline = usage
					continue
				}
				require.True(t, reflect.DeepEqual(usage, baseline), "all typed billing fields must remain unchanged")
				trace := cacheTraceFromGin(c)
				require.Equal(t, tc.bits, trace.data.UsageFields)
				require.Equal(t, tc.prompt, trace.data.UsagePromptPresent)
				if tc.bits&15 != 0 && tc.prompt {
					classification := classifyCacheTrace(cacheTraceClassificationInput{UsageObserved: true,
						UsagePromptPresent: true, UsageFields: trace.data.UsageFields,
						PromptTokens: usage.InputTokens, ReadTokens: usage.CacheReadInputTokens,
						ChainSampled: true, ForkRef: "none", ForkKind: "none", ForkPrecision: "none"}, 1024)
					require.NotEqual(t, "n/a_usage", classification.LossClass, "explicit zero and positive typed reads are present")
				}
			}
		})
	}
}

func BenchmarkCacheTraceResponseMetadataPlainDeltaGate400(b *testing.B) {
	svc := &OpenAIGatewayService{}
	data := []byte(`{"type":"response.output_text.delta","delta":"ordinary output with no observer metadata"}`)
	var usage OpenAIUsage
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for frame := 0; frame < 400; frame++ {
			svc.parseSSEUsageBytesWithTypeIndexed(data, "response.output_text.delta", &usage)
		}
	}
}
