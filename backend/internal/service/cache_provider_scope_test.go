package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheProviderSessionGeminiSources(t *testing.T) {
	for _, source := range []string{"gemini_cli", "gemini_digest", "cacheable_content", "message_content_fallback", "metadata_user_id", "anchor_v2"} {
		t.Run(source, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			engine := &OpenAIGatewayService{cfg: cfg}
			engine.cachePolicies = NewCachePolicyService(nil, cfg, WithProviderCacheTrace())
			traceTestSetPolicy(engine, map[string]any{"enabled": true, "platforms": []string{PlatformGemini}})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			group := int64(101)
			c.Set("api_key", &APIKey{ID: 11, GroupID: &group, Group: &Group{ID: group, Platform: PlatformGemini}})
			gateway := &GatewayService{providerTraceEngine: engine}
			gateway.BeginProviderCacheTrace(c, PlatformGemini, "messages", nil, nil)
			gateway.BindProviderCacheTraceSession(c, "synthetic-session", source)
			trace := cacheTraceFromGin(c)
			require.NotNil(t, trace)
			want := source
			if source == "metadata_user_id" {
				want = "unknown"
			}
			// anchor_v2 is a Gemini provider source once T2.2 is in the build.
			require.Equal(t, want, trace.data.IDSource)
			require.Equal(t, engine.cacheProviderSessionScope(PlatformGemini, 11, group, "synthetic-session").digest, ProviderCacheTraceSessionDigest(c.Request.Context()))
		})
	}
}

func TestCacheProviderSessionTenantAndServingScope(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	const raw = "same-synthetic-provider-session"
	for _, platform := range []string{PlatformAnthropic, PlatformGemini} {
		first := s.cacheProviderSessionScope(platform, 11, 101, raw)
		require.Regexp(t, "^[0-9a-f]{16}$", first.digest)
		require.Equal(t, first, s.cacheProviderSessionScope(platform, 11, 101, raw))
		secondKey := s.cacheProviderSessionScope(platform, 12, 101, raw)
		secondGroup := s.cacheProviderSessionScope(platform, 11, 102, raw)
		require.NotEqual(t, first.digest, secondKey.digest, "same content and group cannot share tenant history")
		require.NotEqual(t, first.digest, secondGroup.digest, "serving-group fallback cannot reuse another group's chain")
		require.NotEqual(t, first, secondKey)
		require.NotEqual(t, first, secondGroup)
		require.NotEqual(t, first, s.cacheProviderSessionScope(platform, 11, 101, raw+"-changed"))
	}
	anthropic := s.cacheProviderSessionScope(PlatformAnthropic, 11, 101, raw)
	gemini := s.cacheProviderSessionScope(PlatformGemini, 11, 101, raw)
	require.NotEqual(t, anthropic.digest, gemini.digest, "platform-session HMAC domains isolate the emitted digests too")
	require.NotEqual(t, anthropic, gemini, "history/LRU keys additionally partition the actual platform")
}

func TestCacheProviderSessionScopeMissingIdentity(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	for _, tc := range []struct {
		platform   string
		key, group int64
		hash       string
	}{
		{PlatformAnthropic, 0, 101, "synthetic"},
		{PlatformAnthropic, -1, 101, "synthetic"},
		{PlatformGemini, 11, -1, "synthetic"},
		{PlatformGemini, 11, 101, ""},
		{PlatformOpenAI, 11, 101, "synthetic"},
		{"", 11, 101, "synthetic"},
	} {
		require.Zero(t, s.cacheProviderSessionScope(tc.platform, tc.key, tc.group, tc.hash))
	}
	for _, missingSecret := range []*OpenAIGatewayService{nil, {}, {cfg: &config.Config{}}} {
		require.Zero(t, missingSecret.cacheProviderSessionScope(PlatformAnthropic, 11, 101, "private-session"),
			"missing HMAC secret must never fall back to a raw identifier")
	}
}

// Authoritative independent vectors bind the algorithm to the contract bytes.
// The negative vector deliberately omits the tenant and serving-group scope.
func TestCacheProviderSessionContractVectors(t *testing.T) {
	type vector struct {
		Platform         string `json:"platform"`
		APIKeyID         int64  `json:"api_key_id"`
		ServingGroupID   int64  `json:"serving_group_id"`
		LocalSessionHash string `json:"local_session_hash"`
		Expected         string `json:"expected"`
		Claimed          string `json:"claimed"`
	}
	var fixture struct {
		Secret  string   `json:"secret"`
		Valid   []vector `json:"valid"`
		Invalid []vector `json:"invalid"`
	}
	raw, err := os.ReadFile("testdata/cache_trace_m2b/session_digest_vectors.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Len(t, fixture.Valid, 4)
	require.Len(t, fixture.Invalid, 1)
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: fixture.Secret}}}
	var history cacheProviderTraceHistory
	caps := CachePolicyCaps{MaxSessions: 4, ChainsPerSession: 3, MaxCheckpoints: 512}
	for _, v := range fixture.Valid {
		t.Run(fmt.Sprintf("%s/%d/%d", v.Platform, v.APIKeyID, v.ServingGroupID), func(t *testing.T) {
			scope := s.cacheProviderSessionScope(v.Platform, v.APIKeyID, v.ServingGroupID, v.LocalSessionHash)
			require.Equal(t, v.Expected, scope.digest)
			token := history.send(scope, uint64(v.APIKeyID), time.Now(), caps)
			require.NotNil(t, token.owner)
			require.Equal(t, v.Expected, history.entries[scope].key.digest,
				"history/LRU must use exactly the emitted digest")
		})
	}
	require.Len(t, history.entries, 4, "all scoped vectors retain independent history")
	for _, v := range fixture.Invalid {
		scope := s.cacheProviderSessionScope(v.Platform, v.APIKeyID, v.ServingGroupID, v.LocalSessionHash)
		require.NotEqual(t, v.Claimed, scope.digest, "unscoped HMAC vector must be rejected")
		require.Contains(t, history.entries, scope, "the scoped key matches its positive vector")
	}
}

func TestCacheProviderGeminiAnchorUsesParts(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	first := []byte(`{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"alpha"}]}]}`)
	second := []byte(`{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"beta"}]}]}`)
	anchor := func(body []byte) string {
		return s.cacheProviderTraceAnchorParts(11, gjson.GetBytes(body, "systemInstruction"), gjson.GetBytes(body, "contents"), PlatformGemini)
	}
	require.Regexp(t, `^[0-9a-f]{16}$`, anchor(first))
	require.NotEqual(t, anchor(first), anchor(second), "Gemini user parts must contribute to the conversation anchor")

	anthropic := []byte(`{"system":"system","messages":[{"role":"user","content":[{"type":"text","text":"alpha"}]}]}`)
	legacy := s.cacheTraceAnchorParts(11, gjson.GetBytes(anthropic, "system"), gjson.GetBytes(anthropic, "messages"))
	provider := s.cacheProviderTraceAnchorParts(11, gjson.GetBytes(anthropic, "system"), gjson.GetBytes(anthropic, "messages"), PlatformAnthropic)
	require.Equal(t, legacy, provider, "Anthropic retains the existing content-based anchor")
}

func TestCacheProviderOwnerSkipUsesExistingDecisionOnly(t *testing.T) {
	trace := &cacheTrace{data: cacheTraceData{Platform: PlatformAnthropic, RouteReason: "sticky_new"}}
	ctx := context.WithValue(context.Background(), cacheTraceKey{}, trace)
	noteProviderCacheTraceOwnerSkip(ctx, "busy_wait_full")
	trace.mu.Lock()
	require.Equal(t, "busy_wait_full", trace.data.Provider.OwnerSkip)
	require.Equal(t, "non_fault_rebind", trace.data.RouteReason)
	trace.mu.Unlock()

	noteProviderCacheTraceOwnerSkip(ctx, "model_unsupported")
	trace.mu.Lock()
	require.Equal(t, "model_unsupported", trace.data.Provider.OwnerSkip)
	require.Equal(t, "non_fault_rebind", trace.data.RouteReason, "model incompatibility does not become a non-fault rebind")
	trace.mu.Unlock()

	noteProviderCacheTraceOwnerSkip(ctx, "not-a-contract-reason")
	trace.mu.Lock()
	require.Equal(t, "model_unsupported", trace.data.Provider.OwnerSkip)
	trace.mu.Unlock()
}

func TestCacheProviderSessionIngressAnchorsAcrossTranslations(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	for _, fixture := range []struct{ protocol, first, second string }{
		{"messages", `{"system":"system","messages":[{"role":"user","content":"alpha"}]}`, `{"system":"system","messages":[{"role":"user","content":"beta"}]}`},
		{"chat", `{"messages":[{"role":"system","content":"system"},{"role":"user","content":"alpha"}]}`, `{"messages":[{"role":"system","content":"system"},{"role":"user","content":"beta"}]}`},
		{"responses", `{"instructions":"system","input":[{"role":"user","content":"alpha"}]}`, `{"instructions":"system","input":[{"role":"user","content":"beta"}]}`},
		{"gemini", `{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"alpha"}]}]}`, `{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"beta"}]}]}`},
	} {
		t.Run(fixture.protocol, func(t *testing.T) {
			first := s.cacheProviderTraceIngressAnchor(11, parseRawJSONView([]byte(fixture.first)), fixture.protocol)
			second := s.cacheProviderTraceIngressAnchor(11, parseRawJSONView([]byte(fixture.second)), fixture.protocol)
			require.Regexp(t, `^[0-9a-f]{16}$`, first)
			require.NotEqual(t, first, second, "first user content must affect the anchor through protocol translation")
		})
	}
}
