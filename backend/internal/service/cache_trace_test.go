package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func traceTestConfig(s *OpenAIGatewayService) {
	s.cachePolicies = NewCachePolicyService(nil, s.cfg)
	traceTestSetPolicy(s, map[string]any{"enabled": true})
}

// Fixture-only publisher: use the production parser and snapshot computation.
func traceTestSetPolicy(s *OpenAIGatewayService, fields map[string]any) {
	if s.cachePolicies == nil {
		s.cachePolicies = NewCachePolicyService(nil, s.cfg)
	}
	spec := map[string]any{}
	for k, v := range s.cachePolicies.Snapshot().entries[1].envelope.Spec {
		spec[k] = v
	}
	for k, v := range fields {
		spec[k] = v
	}
	raw, err := json.Marshal(map[string]any{"rev": s.cachePolicies.Snapshot().ObservabilityRevision() + 1, "spec": spec})
	if err != nil {
		panic(err)
	}
	s.cachePolicies.publish(map[string]string{cachePolicySettings[1]: string(raw)})
}

func init() {
	cacheBillingConfigureTrace = func(s *OpenAIGatewayService, enabled bool) {
		traceTestConfig(s)
		traceTestSetPolicy(s, map[string]any{"enabled": enabled})
	}
}

func TestCacheTraceUUIDAndHeaderPresence(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{}`, "none"}, {`{"prompt_cache_key":""}`, "empty"}, {`{"prompt_cache_key":null}`, "client_non_uuid"},
		{`{"prompt_cache_key":"A63114B0-6F55-0BF9-06A2-FBF1CCE72317"}`, "client_uuid"},
		{`{"prompt_cache_key":" a63114b0-6f55-4bf9-a6a2-fbf1cce72317"}`, "client_non_uuid"},
		{`{"prompt_cache_key":123}`, "client_non_uuid"},
	} {
		require.Equal(t, tc.want, cacheTracePCKIn(gjson.Parse(tc.body).Get("prompt_cache_key")))
	}
	headers := http.Header{}
	for i, name := range cacheTraceHeaders {
		headers.Set(name, "")
		require.Equal(t, uint16((1<<(i+1))-1), cacheTraceHeaderBits(headers))
	}
}

func TestCacheTracePrivacyAndSnapshot(t *testing.T) {
	s, c, req, _ := cacheTraceWireFixture(t, "chat", "apikey", "trace-private-key", "session_id", traceTestConfig)
	payload := []byte(`{"reasoning":{"context":"trace-private-response"},"usage":{"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"prompt_cache_hit_tokens":7,"cache_read_input_tokens":3,"cachedContentTokenCount":4,"prompt_cache_diagnostics":{"key":"trace-private-response"}}}`)
	observeCacheTraceUsage(c, payload)
	result := &OpenAIForwardResult{RequestID: "trace-private-request-id"}
	s.finishCacheTrace(c, result)
	require.NotNil(t, result.CacheTrace)
	require.Equal(t, uint8(63), result.CacheTrace.data.UsageFields)
	before := result.CacheTrace.data
	resetCacheTraceAttempt(c)
	require.Equal(t, before, result.CacheTrace.data, "settlement owns an immutable forward snapshot")
	require.Equal(t, uint8(0), cacheTraceFromGin(c).data.UsageFields)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	result.CacheTrace.emitCacheTrace()
	result.CacheTrace.emitCacheTrace()
	require.Equal(t, 1, strings.Count(logs.String(), `"msg":"cache_trace"`))
	for _, secret := range []string{"trace-private", "synthetic-token", "synthetic-account", "trace-test-secret"} {
		require.NotContains(t, logs.String(), secret)
	}
	require.Len(t, before.ConvAnchor, 16)
	require.Len(t, before.RequestIDHMAC, 64)
	require.Equal(t, cacheTraceFromGin(c).sessionKey, before.SessionDigest)
	require.NotNil(t, req.Body)
}

func TestCacheTracePluginAndBridgeExcluded(t *testing.T) {
	s, c, req, _ := cacheTraceWireFixture(t, "responses", "oauth", "trace-private-key", "none", traceTestConfig)
	s.pluginManager = &PluginManager{}
	s.pluginManager.route.Store(&pluginRoute{rolloutPercent: 100})
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	s.captureCacheTraceRequest(c, req, &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, "responses")
	require.Equal(t, "plugin", cacheTraceFromGin(c).data.Transport)
	require.False(t, cacheTraceFromGin(c).data.IngressWSBridge)
	s.captureCacheTraceRequest(c, req, &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "responses")
	require.Equal(t, "http", cacheTraceFromGin(c).data.Transport)
}

func TestCacheTraceAnchorAndScope(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}}
	c := cacheTestContext(1, 10)
	first := gjson.Parse(`{"instructions":"private-system","input":[{"role":"user","content":"first"}]}`)
	later := gjson.Parse(`{"instructions":"private-system","input":[{"role":"user","content":"first"},{"role":"assistant","content":"reply"},{"role":"user","content":"next"}]}`)
	require.Equal(t, s.cacheTraceAnchor(c, first), s.cacheTraceAnchor(c, later))
	require.NotEqual(t, s.cacheTraceAnchor(c, first), s.cacheTraceAnchor(cacheTestContext(2, 10), first))
	require.NotEqual(t, s.cacheTraceAnchor(c, first), s.cacheTraceAnchor(c, gjson.Parse(`{"instructions":"changed","input":"first"}`)))
}

func TestCacheTraceDisabledHasNoReadsOrAllocations(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}, {JWT: config.JWTConfig{Secret: "synthetic-disabled-secret"}}} {
		s := &OpenAIGatewayService{cfg: cfg}
		if cfg != nil {
			traceTestConfig(s)
		} // enabled policy remains blocked by diagnostics
		c := cacheTestContext(1, 10)
		req := &http.Request{GetBody: func() (io.ReadCloser, error) { panic("disabled trace read body") }}
		body := bytes.Repeat([]byte("not-json"), 25600)
		a := &Account{Platform: PlatformOpenAI}
		allocations := testing.AllocsPerRun(100, func() {
			s.beginCacheTrace(c, body)
			s.captureCacheTraceRequest(c, req, a, "responses")
			s.startCacheTraceRequest(c)
			observeCacheTraceUsage(c, body)
			s.finishCacheTrace(c, nil)
		})
		require.Zero(t, allocations)
		require.Nil(t, cacheTraceFromGin(c))
		require.Nil(t, s.cacheTraceWorkers)
	}
	require.Nil(t, cacheTraceFrom(context.Background()))
}

func BenchmarkCacheTraceDisabled200KB(b *testing.B) {
	s := &OpenAIGatewayService{cfg: &config.Config{}}
	c := cacheTestContext(1, 10)
	body := bytes.Repeat([]byte("not-json"), 25600)
	req := &http.Request{}
	a := &Account{Platform: PlatformOpenAI}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.beginCacheTrace(c, body)
		s.captureCacheTraceRequest(c, req, a, "responses")
		observeCacheTraceUsage(c, body)
	}
}
