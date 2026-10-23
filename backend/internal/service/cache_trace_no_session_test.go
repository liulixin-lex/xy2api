package service

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// A service-layer no-seed fixture. The normal HTTP handler requires model and
// a model alone supplies a content seed, so this is defensive service coverage,
// not an assertion that the public handler accepts model-less requests.
const cacheTraceNoSessionBody = `{"input":[{"type":"function_call_output","call_id":"t3-private-call","output":"t3-private-tool-output"}],"stream":false}`

func TestCacheTraceNoSessionForwardAndSettlement(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("budget_rejected=%t", rejected), func(t *testing.T) {
			body := []byte(cacheTraceNoSessionBody)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("User-Agent", "codex_cli_rs/t3-private-version")
			group := int64(10)
			c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			traceTestConfig(cfg)
			response := traceForwardOK()
			response.Header.Set("X-Request-Id", "t3-private-no-session-request")
			s := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{resp: response}}
			if rejected {
				s.cacheTraceWorkerPool().budget = 1
			}
			account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
			require.Empty(t, deriveOpenAIContentSessionSeed(body))
			require.Empty(t, s.GenerateSessionHash(c, body))
			_, hasSessionKey := c.Get(openAICacheSessionGinKey)
			require.False(t, hasSessionKey, "observation must not manufacture a routing identity")
			require.NotNil(t, cacheTraceFromGin(c))
			require.Empty(t, cacheTraceFromGin(c).sessionKey)
			result, err := s.Forward(c.Request.Context(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.CacheDiagnostic)
			require.Empty(t, result.CacheDiagnostic.Session)
			require.NotNil(t, result.CacheTrace)
			if !rejected {
				awaitCacheTrace(t, s, c, result)
				t3SettlementWorkerDrained(t, s)
				require.True(t, result.CacheTrace.data.Prefix.Sampled)
				require.Regexp(t, `^[0-9a-f]{8,64}$`, result.CacheTrace.data.ConvAnchor, "anchor HMAC does not require a session seed")
			}
			logs := t3SettlementCapture(t)
			s.observeCacheUsage(result, account, time.Now())
			s.settleCacheTrace(c.Request.Context(), result, account)
			s.settleCacheTrace(c.Request.Context(), result, account)
			events := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, events, 1)
			require.Empty(t, t3SettlementEvents(t, logs, "openai_cache_usage"), "legacy no-session behavior stays unchanged")
			event := events[0]
			require.Equal(t, "", event["session_digest"])
			require.Regexp(t, `^[0-9a-f]{64}$`, event["request_id_hmac"])
			require.Equal(t, float64(result.Usage.InputTokens), event["P"])
			require.Equal(t, float64(result.Usage.CacheReadInputTokens), event["read"])
			if rejected {
				require.Equal(t, "n/a_sampled_out", event["loss_class"])
				require.Equal(t, "budget_full", event["na_reason"])
				require.NotContains(t, event, "gap_src")
			} else {
				require.Equal(t, "no_session", event["gap_src"])
				require.NotContains(t, event, "gap_send_ms")
				require.Equal(t, float64(-1), event["window_seq_delta"])
				require.Equal(t, "none", event["fork_ref"])
				require.Equal(t, "unknown", event["id_source"])
			}
			require.Empty(t, s.cacheTelemetry().sessions, "no empty-string session/history entry may be created")
			t3SettlementAssertPrivate(t, logs, result.CacheTrace.chain)
		})
	}
}

func TestCacheTraceNoSessionStillRequiresDiagnostic(t *testing.T) {
	s, c, result := t3SettlementSuccessfulForward(t, "responses", "apikey", false, false)
	trace := result.CacheTrace
	result.CacheDiagnostic = nil
	logs := t3SettlementCapture(t)
	s.settleCacheTrace(c.Request.Context(), result, &Account{ID: 9, Platform: PlatformOpenAI})
	require.Empty(t, logs.String(), "a missing diagnostic object is not an observed empty session")
	require.False(t, trace.settled, "an incomplete result cannot consume the settlement once guard")
}

func TestCacheTraceNoSessionDisabledDoesNotCreateTrace(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(cacheTraceNoSessionBody)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 1})
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	s := &OpenAIGatewayService{cfg: cfg}
	require.Empty(t, s.GenerateSessionHash(c, body))
	require.Nil(t, cacheTraceFromGin(c))
	require.Nil(t, s.cacheTraceWorkers)
	require.Nil(t, s.openaiCacheTelemetry)
}
