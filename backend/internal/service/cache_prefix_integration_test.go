package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCacheTracePrefixPrivacyAndHistory(t *testing.T) {
	s, c, _, body := cacheTraceWireFixture(t, "chat", "apikey", "trace-private-key", "session_id", traceTestConfig)
	trace := cacheTraceFromGin(c)
	require.NotEmpty(t, trace.chain.hashes)
	result := &OpenAIForwardResult{RequestID: "trace-private-request", Usage: OpenAIUsage{InputTokens: 9000}}
	s.finishCacheTrace(c, result)
	require.NotNil(t, result.CacheTrace)
	var log bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	defer slog.SetDefault(old)
	result.CacheTrace.emitCacheTrace()
	for _, marker := range []string{"trace-private-key", "trace-private-request", "trace-private-body", "trace-private-header"} {
		require.NotContains(t, log.String(), marker)
	}
	// Block hashes are ephemeral comparison state, never telemetry identifiers.
	for _, hash := range result.CacheTrace.chain.hashes {
		require.NotContains(t, log.String(), strconv.FormatUint(hash, 10))
		require.NotContains(t, log.String(), fmt.Sprintf("%016x", hash))
	}
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	result.CacheDiagnostic = &OpenAICacheDiagnostic{Session: trace.sessionKey}
	s.observeCacheUsage(result, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now())
	s.rememberCachePrefix(trace.sessionKey, trace.chain, 9000)
	s.captureCachePrefix(trace, body, trace.data.ProtocolOut)
	require.Equal(t, "prev1", trace.data.Prefix.ForkRef)
	require.Equal(t, 9000, trace.data.Prefix.PreviousPrompt)
	require.False(t, trace.data.Prefix.HistoryUnsampled)
	snapshot := result.CacheTrace.chain.hashes[0]
	resetCacheTraceAttempt(c)
	require.Equal(t, snapshot, result.CacheTrace.chain.hashes[0], "attempt reset must not mutate async snapshot")
}

func TestCacheTracePrefixSamplingStableAndHistoryGap(t *testing.T) {
	s, c, _, body := cacheTraceWireFixture(t, "responses", "apikey", "trace-private-key", "none", traceTestConfig)
	trace := cacheTraceFromGin(c)
	for _, percent := range []int{0, 1, 37, 100} {
		s.cfg.Gateway.CacheTrace.ChainPercent = percent
		expected := sha256SumCacheCohort(trace.sessionKey) < percent
		for i := 0; i < 3; i++ {
			s.captureCachePrefix(trace, body, "responses")
			require.Equal(t, expected, trace.data.Prefix.Sampled)
		}
	}
	telemetry := s.cacheTelemetry()
	telemetry.sessions[trace.sessionKey] = openAICacheHistory{at: time.Now()}
	s.captureCachePrefix(trace, body, "responses")
	require.True(t, trace.data.Prefix.HistoryUnsampled)
	trace.data.Transport = "plugin"
	s.captureCachePrefix(trace, body, "responses")
	require.Empty(t, trace.chain.hashes)
	require.Equal(t, -1, trace.data.Prefix.ForkBlock)
}

func TestCacheTracePrefixMemoryBoundAndExistingTelemetry(t *testing.T) {
	s, _, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "none", "none", traceTestConfig)
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	chain, _, _ := prefixTestScan(prefixTestBody(strings.Repeat(`{"role":"user","content":"private-retention"},`, 599) + `{"role":"user","content":"last"}`))
	require.LessOrEqual(t, len(chain.hashes), 512)
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	telemetry := s.cacheTelemetry()
	for i := 0; i < 2051; i++ {
		session := fmt.Sprintf("synthetic-%d", i)
		result := &OpenAIForwardResult{CacheDiagnostic: &OpenAICacheDiagnostic{Session: session}, Usage: OpenAIUsage{InputTokens: 9000}}
		s.observeCacheUsage(result, account, time.Unix(int64(i+1), 0))
		for j := 0; j < 4; j++ {
			own := chain
			own.hashes = append([]uint64(nil), chain.hashes...)
			s.rememberCachePrefix(session, own, 9000+j)
		}
		// Ordinary diagnostics updates must preserve all three trace histories.
		s.observeCacheUsage(result, account, time.Unix(int64(i+1), 0))
	}
	require.Len(t, telemetry.sessions, 2048)
	retained := 0
	for _, session := range telemetry.sessions {
		for i, chain := range session.traceChains {
			require.LessOrEqual(t, len(chain.hashes), 512)
			require.Equal(t, 9003-i, chain.prompt)
			retained += cap(chain.hashes) * 8
		}
	}
	require.LessOrEqual(t, retained, 2048*3*512*8)
	// Observation adds no context/state to unrelated schedulers.
	require.Nil(t, cacheTraceFrom(context.Background()))
}

func TestCacheTracePrefixCapturesFinalLiteContext(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(strconv.FormatBool(passthrough), func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6-terra","stream":true,"instructions":"trace-private-system","reasoning":{"context":"current_turn"},"input":[{"role":"user","content":"trace-private-user"}]}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
			c.Request.Header.Set(responsesLiteHeader, "true")
			group := int64(10)
			c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_lite_trace\",\"usage\":{\"input_tokens\":12000,\"output_tokens\":1}}}\n\n"))}}
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			traceTestConfig(cfg)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			account := &Account{ID: 501, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}, Extra: map[string]any{"openai_passthrough": passthrough}}
			svc.GenerateSessionHash(c, body)
			result, err := svc.Forward(c.Request.Context(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result.CacheTrace)
			awaitCacheTrace(t, svc, c, result)
			require.Equal(t, "all_turns", result.CacheTrace.data.Prefix.ReasoningContext, "observe final wire after Lite normalization")
		})
	}
}
