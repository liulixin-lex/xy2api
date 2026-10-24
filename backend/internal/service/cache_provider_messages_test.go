//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cacheProviderMessagesPolicy(s *OpenAIGatewayService, platforms []string, enabled bool, sample int) {
	s.cachePolicies = NewCachePolicyService(nil, s.cfg, WithCacheNorthstar(), WithProviderCacheTrace())
	traceTestSetPolicy(s, map[string]any{"enabled": enabled, "platforms": platforms, "sample_percent": sample, "chain_percent": 100})
	s.cacheCounters = NewCacheTraceCounters()
}

func TestCacheProviderMessagesGateWireAndSettlement(t *testing.T) {
	raw, err := os.ReadFile(cacheProviderMessagesGoldenPath())
	require.NoError(t, err)
	baseline := map[string]cacheProviderMessagesGolden{}
	require.NoError(t, json.Unmarshal(raw, &baseline))
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"off", "openai_only", "anthropic_only", "messages", "all", "sample_zero", "kill", "diagnostics_off"} {
			t.Run(fmt.Sprintf("%t/%s", stream, mode), func(t *testing.T) {
				logs := t3SettlementCapture(t)
				admitted := mode == "messages" || mode == "all"
				business := admitted || mode == "sample_zero"
				f, result, wire := cacheProviderMessagesForward(t, stream, func(s *OpenAIGatewayService) {
					s.cfg.Gateway.CacheFeatures.KillSwitch = mode == "kill"
					s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = mode != "diagnostics_off"
					platforms := []string{"messages_bridge"}
					switch mode {
					case "openai_only":
						platforms = []string{"openai"}
					case "anthropic_only":
						platforms = []string{"anthropic"}
					case "all":
						platforms = []string{"openai", "anthropic", "gemini", "messages_bridge"}
					}
					sample := 100
					if mode == "sample_zero" {
						sample = 0
					}
					cacheProviderMessagesPolicy(s, platforms, mode != "off", sample)
				})
				require.Equal(t, baseline[fmt.Sprint(stream)].Wire, wire)
				require.Equal(t, baseline[fmt.Sprint(stream)].Usage, result.Usage)
				if admitted {
					require.NotNil(t, result.CacheTrace)
					require.Equal(t, "messages", result.CacheTrace.data.ProtocolIn)
					require.Equal(t, "responses", result.CacheTrace.data.ProtocolOut)
					require.True(t, result.CacheTrace.data.UsagePromptObserved)
					require.True(t, result.CacheTrace.data.UsagePromptPresent)
					require.NotZero(t, result.CacheTrace.data.UsageFields&1)
					require.NotZero(t, result.CacheTrace.timing.sequence)
					awaitCacheTrace(t, f.s, f.c, result)
					t3SettlementWorkerDrained(t, f.s)
				} else {
					require.Nil(t, result.CacheTrace)
					require.Nil(t, result.cacheTraceFailure)
					require.Nil(t, f.s.cacheTraceWorkers, "excluded platforms never allocate a body worker")
				}
				billing := cacheProviderMessagesBilling(t, f, result)
				require.Equal(t, baseline[fmt.Sprint(stream)].Billing, billing)
				events := t3SettlementEvents(t, logs, "cache_trace")
				if admitted {
					require.Len(t, events, 1)
					event := events[0]
					require.Equal(t, "openai", event["platform"])
					require.Equal(t, "messages", event["protocol_in"])
					require.Equal(t, "responses", event["protocol_out"])
					require.Equal(t, "gw_isolated", event["sess_out_relation"])
					require.EqualValues(t, result.Usage.InputTokens, event["P"])
					require.EqualValues(t, result.Usage.CacheReadInputTokens, event["read"])
					usage := t3SettlementEvents(t, logs, "openai_cache_usage")
					require.Len(t, usage, 1)
					t3SettlementAssertUsageLink(t, event, usage[0], result, f.account)
					if output := os.Getenv("CACHE_PROVIDER_MESSAGES_EVENTS"); output != "" {
						require.NoError(t, os.MkdirAll(output, 0700))
						encoded, err := json.Marshal(event)
						require.NoError(t, err)
						require.NoError(t, os.WriteFile(fmt.Sprintf("%s/%t-%s.json", output, stream, mode), encoded, 0600))
					}
				} else {
					require.Empty(t, events)
				}
				if business {
					require.NotNil(t, result.cachePanel)
					view := f.s.cacheCounters.Snapshot(time.Now().UTC(), CachePanelQuery{Window: "1h", Platform: "openai"}, f.s.cachePolicies.Snapshot())
					coverage := view["coverage"].(map[string]any)
					require.EqualValues(t, 1, coverage["business_requests"])
					if admitted {
						require.EqualValues(t, 1, coverage["traced_requests"])
					} else {
						require.EqualValues(t, 0, coverage["traced_requests"])
					}
				} else {
					require.Nil(t, result.cachePanel)
					for _, bucket := range f.s.cacheCounters.minutes {
						require.Nil(t, bucket)
					}
				}
				require.Nil(t, f.s.cacheCounters.providers[0])
				require.Nil(t, f.s.cacheCounters.providers[1])
				encoded, err := json.Marshal(events)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "ctc-private")
			})
		}
	}
}

func TestCacheProviderMessagesNativeSubpathExcluded(t *testing.T) {
	f := cacheTraceNewContractFixture(t, "native-excluded", "responses")
	cacheProviderMessagesPolicy(f.s, []string{"messages_bridge"}, true, 100)
	f.c.Request.URL.Path = "/v1/messages"
	f.body = []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic"}],"stream":false,"max_tokens":32}`)
	f.account.Credentials["api_protocol"] = APIProtocolAnthropic
	f.account.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "http://anthropic.example"}
	f.s.httpUpstream = &httpUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}
	f.s.GenerateSessionHash(f.c, f.body)
	result, err := f.s.ForwardAsAnthropic(f.c.Request.Context(), f.c, f.account, f.body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.CacheTrace)
	require.Nil(t, result.cachePanel)
	require.Nil(t, f.s.cacheTraceWorkers)
}

func TestCacheProviderMessagesStreamingTypedUsagePresence(t *testing.T) {
	for _, usage := range []struct {
		raw     string
		present bool
		read    int
	}{
		{`{"input_tokens":0,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}`, true, 0},
		{`{"output_tokens":1,"input_tokens_details":{"cached_tokens":4}}`, false, 4},
	} {
		t.Run(fmt.Sprint(usage.present), func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "typed-bridge", "responses")
			cacheProviderMessagesPolicy(f.s, []string{"messages_bridge"}, true, 100)
			f.c.Request.URL.Path = "/v1/messages"
			f.s.GenerateSessionHash(f.c, f.body)
			SetActualOpenAIUpstreamEndpoint(f.c, openAIResponsesUpstreamEndpoint)
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(cacheProviderMessagesUsageSSE(usage.raw)))}
			result, err := f.s.handleAnthropicStreamingResponse(response, f.c, f.account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, usage.read, result.Usage.CacheReadInputTokens)
			trace := cacheTraceFromGin(f.c)
			require.NotNil(t, trace)
			require.Equal(t, usage.present, trace.data.UsagePromptPresent)
			require.EqualValues(t, 1, trace.data.UsageFields&1)
			require.Zero(t, result.Usage.InputTokens)
		})
	}
}

func TestCacheProviderOpenAIExcludedPlatformZeroWork(t *testing.T) {
	f := cacheTraceNewContractFixture(t, "platform-excluded", "responses")
	cacheProviderMessagesPolicy(f.s, []string{"anthropic"}, true, 100)
	req, err := http.NewRequest(http.MethodPost, "http://synthetic.example/v1/responses", bytes.NewReader(f.body))
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) { panic("excluded OpenAI read body") }
	f.s.GenerateSessionHash(f.c, f.body)
	allocations := testing.AllocsPerRun(100, func() {
		f.s.beginCacheTrace(f.c, f.body)
		f.s.captureCacheTraceRequest(f.c, req, f.account, "responses")
		f.s.markCacheTraceSent(f.c)
		f.s.startCacheTraceRequest(f.c)
		f.s.finishCacheTrace(f.c, nil)
	})
	require.Zero(t, allocations)
	require.Nil(t, cacheTraceFromGin(f.c))
	require.Nil(t, f.s.cacheTraceWorkers)
}
