//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Real response handlers and final billed usage, not a synthetic read value.
func TestCacheProviderAliasOnlySettlement(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", protocol, stream), func(t *testing.T) {
				const before = `"input_tokens_details":{"cached_tokens":1024},`
				const after = `"prompt_cache_hit_tokens":1024,`
				require.Equal(t, 1, strings.Count(traceForwardSSE, before))
				reply := strings.Replace(traceForwardSSE, before, after, 1)
				var f *cacheTraceContractFixture
				var result *OpenAIForwardResult
				if protocol == "messages" {
					f, result, _ = cacheProviderMessagesForward(t, stream, func(s *OpenAIGatewayService) {
						cacheProviderMessagesPolicy(s, []string{"messages_bridge"}, true, 100)
					}, reply)
					require.NotNil(t, result.CacheTrace)
					awaitCacheTrace(t, f.s, f.c, result)
					t3SettlementWorkerDrained(t, f.s)
				} else {
					s, c, forwarded, _ := t3SettlementForwardReply(t, protocol, stream, reply, false)
					f, result = &cacheTraceContractFixture{s: s, c: c}, forwarded
				}
				require.Equal(t, 2000, result.Usage.InputTokens)
				require.Zero(t, result.Usage.CacheReadInputTokens, "billing remains unchanged")
				require.EqualValues(t, 2, result.CacheTrace.data.UsageFields&15)
				account := &Account{ID: 9, Platform: PlatformOpenAI}
				logs := t3SettlementCapture(t)
				f.s.observeCacheUsage(result, account, time.Now())
				f.s.settleCacheTrace(f.c.Request.Context(), result, account)
				events := t3SettlementEvents(t, logs, "cache_trace")
				usages := t3SettlementEvents(t, logs, "openai_cache_usage")
				require.Len(t, events, 1)
				require.Len(t, usages, 1)
				event := events[0]
				require.Equal(t, "n/a_usage", event["loss_class"])
				require.NotContains(t, event, "na_reason")
				require.Equal(t, false, event["rsn_boundary_hit"])
				require.Equal(t, false, event["read_short_of_fork"])
				t3SettlementAssertUsageLink(t, event, usages[0], result, account)
				if output := os.Getenv("CACHE_PROVIDER_ALIAS_EVENTS"); output != "" {
					require.NoError(t, os.MkdirAll(output, 0700))
					encoded, err := json.Marshal(event)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(output,
						fmt.Sprintf("%s-%t.json", protocol, stream)), encoded, 0600))
				}
			})
		}
	}
}
