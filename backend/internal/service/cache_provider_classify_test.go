//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Drive the real provider settlement and emitter with an independent synthetic
// boundary: 4096 prompt tokens / 4096 logical bytes, first thinking byte 1024.
// This tests classification wiring without claiming a real upstream fork.
func TestCacheProviderThinkingStripSettlementConditions(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*cacheTraceData)
	}{
		{"all-conditions", "provider_thinking_strip", func(*cacheTraceData) {}},
		{"model-preserves-history", "under_read", func(d *cacheTraceData) { d.Provider.lastTurnOnly = false }},
		{"tool-tail", "under_read", func(d *cacheTraceData) { d.Prefix.TailKind = "tool_output" }},
		{"no-thinking", "under_read", func(d *cacheTraceData) { d.Provider.ThinkingBlocks = 0 }},
		{"unknown-boundary", "under_read", func(d *cacheTraceData) { d.Provider.FirstThinkingByte = -1 }},
		{"outside-boundary-tolerance", "under_read", func(d *cacheTraceData) { d.Provider.FirstThinkingByte = 2048 }},
		{"not-anthropic", "under_read", func(d *cacheTraceData) { d.Platform = PlatformGemini }},
		{"cold-history", "cold", func(d *cacheTraceData) { d.Prefix.ForkRef = "none" }},
		{"unknown-read", "n/a_usage", func(d *cacheTraceData) { d.UsageFields = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := cacheTraceData{
				Platform: PlatformAnthropic, ProtocolIn: "messages", ProtocolOut: "messages", Transport: "http",
				UsagePromptObserved: true, UsagePromptPresent: true, UsageFields: 1,
				Prefix: cachePrefixObservation{Sampled: true, BytesOut: 4096, BytesOutRaw: 4096,
					PreviousPrompt: 4096, ForkRef: "prev1", ForkKind: "cur_prefix", ForkPrecision: "exact", TailKind: "user_msg"},
				Provider: cacheProviderFields{lastTurnOnly: true, ThinkingBlocks: 1, FirstThinkingByte: 1024},
			}
			tc.change(&data)
			trace := &cacheTrace{data: data}
			result := &ForwardResult{providerTrace: trace, Usage: ClaudeUsage{InputTokens: 3072, CacheReadInputTokens: 1024}}
			logs := t3SettlementCapture(t)
			engine := &OpenAIGatewayService{}
			engine.settleProviderCacheTrace(result, cacheTraceDelivered, true)
			events := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, events, 1)
			require.Equal(t, tc.want, events[0]["loss_class"])
			require.Equal(t, float64(4096), events[0]["P"])
			require.Equal(t, float64(1024), events[0]["read"])
			require.True(t, trace.settled)
		})
	}
}
