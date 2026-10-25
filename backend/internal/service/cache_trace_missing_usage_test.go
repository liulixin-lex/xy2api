package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func t3MissingUsageSSE(t *testing.T) string {
	t.Helper()
	const usage = `,"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1024},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2001}`
	require.Equal(t, 1, strings.Count(traceForwardSSE, usage))
	wire := strings.Replace(traceForwardSSE, usage, "", 1)
	require.NotContains(t, wire, `"usage"`)
	return wire
}

func t3AssertMissingUsageSettlement(t *testing.T, s *OpenAIGatewayService, c *gin.Context, result *OpenAIForwardResult) {
	t.Helper()
	require.NotNil(t, result)
	require.NotNil(t, result.CacheTrace)
	require.NotNil(t, result.CacheDiagnostic)
	trace := result.CacheTrace
	require.True(t, trace.data.UsagePromptObserved, "successful observation is known even with no usage object")
	require.False(t, trace.data.UsagePromptPresent)
	require.Zero(t, trace.data.UsageFields)
	require.Equal(t, OpenAIUsage{}, result.Usage, "absence must not invent billing counts")
	logs := t3SettlementCapture(t)
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	s.observeCacheUsage(result, account, time.Now())
	s.settleCacheTrace(c.Request.Context(), result, account)
	first := logs.String()
	s.settleCacheTrace(c.Request.Context(), result, account)
	require.Equal(t, first, logs.String())
	events := t3SettlementEvents(t, logs, "cache_trace")
	usageEvents := t3SettlementEvents(t, logs, "openai_cache_usage")
	require.Len(t, events, 1)
	require.Len(t, usageEvents, 1)
	payload := t3SettlementPayload(t, events[0], false, CachePolicyFromGin(c).Revision())
	require.Equal(t, "n/a_usage", payload["loss_class"])
	require.NotContains(t, payload, "na_reason", "an observed absence is not a not_ready observation gap")
	require.Equal(t, float64(0), payload["P"])
	require.Equal(t, float64(0), payload["read"])
	require.Equal(t, float64(-1), payload["tok_per_byte_milli"])
	t3SettlementAssertUsageLink(t, events[0], usageEvents[0], result, account)
	owner := s.cacheTelemetry().sessions[result.CacheDiagnostic.Session].traceChains
	if owner != nil {
		for _, chain := range owner.chains {
			require.Empty(t, chain.hashes, "missing P cannot publish an invented zero-token history")
		}
	}
	t3SettlementAssertPrivate(t, logs, trace.chain)
}

// Exercise actual Forward and Chat->Responses paths, including terminal EOF.
// The synthetic upstream has visible output and no usage in any frame; it is
// an existing successful response, not an empty terminal refusal.
func TestCacheTraceMissingUsageForwardSettlement(t *testing.T) {
	wire := t3MissingUsageSSE(t)
	for _, protocol := range []string{"responses", "chat"} {
		for _, stream := range []bool{false, true} {
			for _, eof := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/eof=%t", protocol, stream, eof), func(t *testing.T) {
					reply := wire
					if eof {
						reply = strings.TrimRight(reply, "\n")
					}
					s, c, result, _ := t3SettlementForwardReply(t, protocol, stream, reply, false)
					require.Equal(t, s.parseSSEUsageFromBody(reply), &result.Usage, "unchanged parser bills the same zero usage")
					t3AssertMissingUsageSettlement(t, s, c, result)
				})
			}
		}
	}
}

// Native Responses' original plain JSON path rejects an absent usage object.
// Raw CC permits it. Preserve both contracts rather than altering the original
// billing parser to manufacture a successful native Responses response.
func TestCacheTraceMissingUsagePlainForwardBillingParity(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw_cc=%t", raw), func(t *testing.T) {
			var offUsage OpenAIUsage
			var offError string
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
					field, path := "input", "/v1/responses"
					reply := `{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`
					if raw {
						field, path = "messages", "/v1/chat/completions"
						reply = `{"id":"resp_1","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
					}
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","%s":[{"role":"user","content":"t3-private-body"}],"prompt_cache_key":"t3-private-key","stream":false}`, field))
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")
					c.Request.Header.Set("session-id", "t3-private-session")
					group := int64(10)
					c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
					cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
					cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"t3-private-upstream-request"}}, Body: io.NopCloser(strings.NewReader(reply))}
					s := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{resp: resp}}
					traceTestConfig(s)
					traceTestSetPolicy(s, map[string]any{"enabled": enabled})
					account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": !raw}}
					s.GenerateSessionHash(c, body)
					var result *OpenAIForwardResult
					var err error
					if raw {
						result, err = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
					} else {
						result, err = s.Forward(c.Request.Context(), c, account, body)
					}
					if enabled {
						awaitCacheTrace(t, s, c, result)
						t3SettlementWorkerDrained(t, s)
					}
					if !raw {
						require.Error(t, err)
						require.Nil(t, result)
						if !enabled {
							offError = err.Error()
						} else {
							require.Equal(t, offError, err.Error(), "native JSON error return is unchanged")
							logs := t3SettlementCapture(t)
							s.settleCacheTrace(c.Request.Context(), result, account)
							require.Empty(t, t3SettlementEvents(t, logs, "cache_trace"))
						}
						return
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					if !enabled {
						require.Nil(t, result.CacheTrace)
						offUsage = result.Usage
						return
					}
					require.Equal(t, offUsage, result.Usage)
					require.Equal(t, cacheTraceBillingForUsage(t, offUsage), cacheTraceBillingForUsage(t, result.Usage))
					t3AssertMissingUsageSettlement(t, s, c, result)
				})
			}
		})
	}
}

func TestCacheTraceMissingUsageObservationGateLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name             string
		before, after    bool
		enter            bool
		wantObserved     bool
		wantLoss, wantNA string
	}{
		{"ready_but_never_entered", false, false, false, false, "n/a_sampled_out", "not_ready"},
		{"expired_before_entry", true, false, true, false, "n/a_sampled_out", "expired"},
		{"expired_after_entry", false, true, true, true, "n/a_usage", ""},
		{"ready_observed_absence", false, false, true, true, "n/a_usage", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "t3-private-key", "session_id", traceTestConfig)
			t3SettlementWorkerDrained(t, s)
			trace := cacheTraceFromGin(c)
			require.False(t, trace.data.UsagePromptObserved, "sending a request is not response observation")
			if tc.before {
				trace.async = &cacheTraceAsync{}
				trace.async.reason.Store(cacheTraceExpired)
			}
			if tc.enter {
				require.Equal(t, !tc.before, cacheTraceObserving(c))
			}
			if tc.after {
				trace.async = &cacheTraceAsync{}
				trace.async.reason.Store(cacheTraceExpired)
				require.True(t, cacheTraceObserving(c), "adapters retain the original enabled decision after expiration")
				// The nested JSON/buffered helper gate must remain enabled too.
				usage, ok := extractOpenAIUsageForCacheTrace(c, []byte(`{"id":"resp_1","status":"completed"}`))
				require.False(t, ok)
				require.Equal(t, OpenAIUsage{}, usage)
				require.Equal(t, &OpenAIUsage{}, s.parseSSEUsageWithCacheTrace(c, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))
			}
			result := &OpenAIForwardResult{}
			s.finishCacheTrace(c, result)
			require.NotNil(t, result.CacheTrace)
			d := result.CacheTrace.data
			require.Equal(t, tc.wantObserved, d.UsagePromptObserved)
			require.False(t, d.UsagePromptPresent)
			require.Zero(t, d.UsageFields)
			classification := classifyCacheTrace(cacheTraceClassificationInput{Transport: d.Transport,
				UsageObserved: d.UsagePromptObserved, UsagePromptPresent: d.UsagePromptPresent, UsageFields: d.UsageFields,
				CoverageReason: d.NAReason, ChainSampled: d.Prefix.Sampled}, 1024)
			require.Equal(t, tc.wantLoss, classification.LossClass)
			require.Equal(t, tc.wantNA, classification.NAReason)
			resetCacheTraceAttempt(c)
			require.False(t, trace.data.UsagePromptObserved)
			trace.async = &cacheTraceAsync{}
			trace.async.reason.Store(cacheTraceExpired)
			require.False(t, cacheTraceObserving(c), "a previous accepted attempt must not activate a new expired attempt")
		})
	}
}
