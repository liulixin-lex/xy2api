package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCacheTraceUsageFastPathPreservesProgressivePresence(t *testing.T) {
	for _, tc := range []struct {
		body string
		want uint8
	}{
		{`{"type":"response.output_text.delta","delta":"private-output-marker"}`, 0},
		{`{"type":"response.output_text.delta","usage":{"input_tokens_details":{"cached_tokens":0}}}`, 1},
		{`{"type":"response.output_text.delta","response":{"usage":{"cache_read_input_tokens":123}}}`, 4},
		{`{"data":{"response":{"usage":{"prompt_cache_hit_tokens":0}}}}`, 2},
		{`{"prompt_cache_diagnostics":"private-unrecognized-reason"}`, 32},
	} {
		_, c, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "private-pck", "session_id", traceTestConfig)
		observeCacheTraceUsageString(c, tc.body)
		require.Equal(t, tc.want, cacheTraceFromGin(c).data.UsageFields)
	}
}

func TestCacheTraceBufferedSinglePassMatchesBilling(t *testing.T) {
	for _, body := range []string{
		traceAllocationSSE(200, false),
		"data: " + `{"type":"response.output_text.delta","usage":{"input_tokens":4096,"input_tokens_details":{"cached_tokens":1024}}}` + "\n\n",
		"data: " + `{"response":{"usage":{"input_tokens":4096,"input_tokens_details":{"cached_tokens":0}}}}` + "\n\n",
	} {
		s, c, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "private-pck", "session_id", traceTestConfig)
		expected := s.parseSSEUsageFromBody(body)
		got := s.parseSSEUsageWithCacheTrace(c, body)
		require.Equal(t, expected, got)
		require.Equal(t, uint8(1), cacheTraceFromGin(c).data.UsageFields&15)
	}
}
