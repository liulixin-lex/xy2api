package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func cacheTestContext(key, group int64) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("session-id", "client-session-secret")
	c.Set("api_key", &APIKey{ID: key, GroupID: &group})
	return c
}

func TestOpenAICacheIdentityTenantScope(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "persistent-test-secret"}}}
	body := []byte(`{"model":"gpt-5.4","instructions":"private prompt","input":"hello"}`)
	first := cacheTestContext(1, 10)
	hash := s.GenerateSessionHash(first, body)
	require.True(t, strings.HasPrefix(hash, "v2:"))
	require.Empty(t, openAILegacySessionHashFromContext(first.Request.Context()))
	require.Equal(t, hash, s.GenerateSessionHash(cacheTestContext(1, 10), body))
	require.NotEqual(t, hash, s.GenerateSessionHash(cacheTestContext(2, 10), body))
	require.NotEqual(t, hash, s.GenerateSessionHash(cacheTestContext(1, 11), body))
	require.NotEqual(t, hash, s.GenerateSessionHash(cacheTestContext(1, 10), []byte(`{"model":"gpt-6.1-sol"}`)))
	require.Equal(t, "client-session-secret", first.GetHeader("session-id"))
	require.Equal(t, hash, s.GenerateExplicitSessionHash(cacheTestContext(1, 10), body))
	require.Empty(t, s.GenerateSessionHashWithFallback(nil, nil, "fallback"))
}

func TestOpenAICacheStableLeadingPrefix(t *testing.T) {
	first := []byte(`{"model":"gpt-5.4","input":[{"role":"developer","content":"stable"},{"role":"user","content":"hello"}]}`)
	later := []byte(`{"model":"gpt-5.4","input":[{"role":"developer","content":"stable"},{"role":"user","content":"hello"},{"role":"assistant","content":"world"},{"role":"system","content":"dynamic"}]}`)
	require.Equal(t, deriveOpenAIContentSessionSeed(first), deriveOpenAIContentSessionSeed(later))
	require.Equal(t, openAICachePrefix(first), openAICachePrefix(later))
	require.NotEqual(t, openAICachePrefix(first), openAICachePrefix([]byte(`{"input":[{"role":"developer","content":"changed"},{"role":"user","content":"hello"}]}`)))
}

func TestOpenAICacheCapabilityIgnoresUserAgent(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","prompt_cache_key":"explicit-key","prompt_cache_retention":"24h","prompt_cache_options":{"ttl":"30m"},"safety_identifier":"safety"}`)
	api := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	actual, err := normalizeOpenAICacheControls(api, body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(actual))
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	actual, err = normalizeOpenAICacheControls(oauth, body)
	require.NoError(t, err)
	for _, key := range []string{"prompt_cache_retention", "prompt_cache_options", "safety_identifier"} {
		require.False(t, gjson.GetBytes(actual, key).Exists())
	}
	require.Equal(t, "explicit-key", gjson.GetBytes(actual, "prompt_cache_key").String())
}

func TestOpenAICacheDiagnosticUsesFinalRequest(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "persistent-test-secret"}}}
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	for _, tc := range []struct {
		protocol GroupPromptProtocol
		body     string
	}{
		{GroupPromptResponses, `{"model":"gpt-5.4","instructions":"client","input":"hello"}`},
		{GroupPromptChat, `{"model":"gpt-5.4","messages":[{"role":"system","content":"client"},{"role":"user","content":"hello"}]}`},
	} {
		c := cacheTestContext(1, 10)
		s.GenerateSessionHash(c, []byte(tc.body))
		ingress := s.newCacheDiagnostic(c, []byte(tc.body), "ingress")
		ctx := WithGroupSystemPrompt(c.Request.Context(), GroupSystemPromptConfig{Prompt: "admin policy"}, "gpt-5.4")
		req, err := newGroupPromptUpstreamRequest(ctx, "POST", "https://example.invalid/v1/responses", []byte(tc.body), tc.protocol)
		require.NoError(t, err)
		s.captureCacheRequestDiagnostic(c, req)
		result := &OpenAIForwardResult{UpstreamModel: "gpt-5.4"}
		s.finishCacheDiagnostic(c, []byte(tc.body), result)
		require.NotNil(t, result.CacheDiagnostic)
		require.Equal(t, "wire", result.CacheDiagnostic.Source)
		require.Equal(t, ingress.Prefix, result.CacheDiagnostic.Prefix)
		require.NotEqual(t, ingress.WirePrefix, result.CacheDiagnostic.WirePrefix)
		encoded, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Contains(t, string(encoded), "admin policy", "diagnostics must not consume the forwarded body")
	}
}

func TestOpenAICacheDiagnosticPrivacyAndTraining(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "persistent-test-secret"}}}
	c := cacheTestContext(1, 10)
	body := []byte(`{"model":"gpt-5.4","instructions":"private secret prompt","prompt_cache_key":"private key","prompt_cache_options":{"mode":"private mode"},"text":{"format":{"type":"private format"}},"input":"dynamic secret"}`)
	require.Nil(t, s.newCacheDiagnostic(c, body, "wire"), "disabled paths must not fingerprint large bodies")
	s.cfg.Gateway.OpenAICache.AwareRoutingEnabled = true
	s.cfg.Gateway.OpenAICache.RolloutPercent = 100
	s.GenerateSessionHash(c, body)
	d := s.newCacheDiagnostic(c, body, "wire")
	require.NotNil(t, d)
	require.Equal(t, "other", d.Mode)
	require.Equal(t, "other", d.Format)
	encoded, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	require.NotContains(t, string(encoded), "secret")
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	r := &OpenAIForwardResult{Model: "gpt-5.4", CacheDiagnostic: d, Usage: OpenAIUsage{InputTokens: 2048, CacheReadInputTokens: 1536, CacheReadSource: "cache_read_tokens"}}
	ctx := context.WithValue(context.Background(), openAICacheDiagnosticKey{}, d)
	now := time.Now()
	s.observeCacheUsage(r, a, now)
	_, known := s.cachePreference(ctx, a, "gpt-5.4")
	require.False(t, known, "a cold request is not a comparable cache sample")
	for i := 0; i < 20; i++ {
		s.observeCacheUsage(r, a, now.Add(time.Duration(i+1)*time.Millisecond))
	}
	_, known = s.cachePreference(ctx, a, "gpt-5.4")
	require.True(t, known)
	before := s.cacheTelemetry().scores[cacheSampleKey(d, a, "gpt-5.4")].count
	s.observeCacheUsage(r, a, now.Add(31*time.Minute))
	require.Equal(t, before, s.cacheTelemetry().scores[cacheSampleKey(d, a, "gpt-5.4")].count, "expired sessions cannot train preferences")
	s.finishCacheDiagnostic(nil, nil, r)
}

func TestOpenAICachePreferencePreservesPriorityLoadAndUnknowns(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{}}
	s.cfg.Gateway.OpenAICache = config.OpenAICacheConfig{AwareRoutingEnabled: true, RolloutPercent: 100}
	d := &OpenAICacheDiagnostic{Session: "v2:test", Prefix: "prefix", Model: "gpt-5.4", GroupID: 10}
	ctx := context.WithValue(context.Background(), openAICacheDiagnosticKey{}, d)
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1}
	b := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1}
	c := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 2}
	telemetry := s.cacheTelemetry()
	telemetry.scores[cacheSampleKey(d, a, "gpt-5.4")] = openAICacheSample{count: 20, input: 2048, at: time.Now()}
	telemetry.scores[cacheSampleKey(d, b, "gpt-5.4")] = openAICacheSample{count: 20, input: 2048, read: 1800, at: time.Now()}
	telemetry.scores[cacheSampleKey(d, c, "gpt-5.4")] = openAICacheSample{count: 20, input: 2048, read: 2000, at: time.Now()}
	available := []accountWithLoad{{account: a, loadInfo: &AccountLoadInfo{}}, {account: b, loadInfo: &AccountLoadInfo{}}, {account: c, loadInfo: &AccountLoadInfo{}}}
	s.preferCacheCandidates(ctx, available, "gpt-5.4")
	require.Equal(t, int64(2), available[0].account.ID)
	require.Equal(t, int64(3), available[2].account.ID)
	available[0], available[1] = available[1], available[0]
	available[1].loadInfo.LoadRate = 10
	s.preferCacheCandidates(ctx, available, "gpt-5.4")
	require.Equal(t, int64(1), available[0].account.ID)
	available[1].loadInfo.LoadRate = 0
	delete(telemetry.scores, cacheSampleKey(d, b, "gpt-5.4"))
	s.preferCacheCandidates(ctx, available, "gpt-5.4")
	require.Equal(t, int64(1), available[0].account.ID, "incomplete cohorts must keep the established order")
}

type atomicStickyTestCache struct {
	GatewayCache
	mu       sync.Mutex
	bindings map[string]int64
}

func (c *atomicStickyTestCache) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner := c.bindings[key]; owner > 0 {
		return owner, nil
	}
	return 0, ErrStickySessionNotFound
}

func (c *atomicStickyTestCache) ClaimSessionAccountID(_ context.Context, _ int64, key string, owner int64, _ time.Duration, _ bool) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bindings[key] == 0 {
		c.bindings[key] = owner
	}
	return c.bindings[key], nil
}

func (c *atomicStickyTestCache) DeleteSessionAccountIDIfOwner(_ context.Context, _ int64, key string, owner int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bindings[key] == owner {
		delete(c.bindings, key)
	}
	return nil
}

func TestOpenAICacheConcurrentColdSelection(t *testing.T) {
	t.Run("ordinary sticky cache", func(t *testing.T) { testOpenAICacheConcurrentColdSelection(t, false) })
	t.Run("quality snapshot without an owner", func(t *testing.T) { testOpenAICacheConcurrentColdSelection(t, true) })
}

func testOpenAICacheConcurrentColdSelection(t *testing.T, qualitySnapshot bool) {
	t.Helper()
	group := int64(10)
	cache := &atomicStickyTestCache{bindings: make(map[string]int64)}
	s := &OpenAIGatewayService{cache: cache, cfg: &config.Config{RunMode: config.RunModeStandard}, accountRepo: stubOpenAIAccountRepo{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 100, Priority: 1, GroupIDs: []int64{group}},
		{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 100, Priority: 1, GroupIDs: []int64{group}},
	}}, concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}
	s.cfg.Gateway.Scheduling.LoadBatchEnabled = true
	start := make(chan struct{})
	results := make(chan *AccountSelectionResult, 30)
	errors := make(chan error, 30)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx := context.Background()
			if qualitySnapshot {
				ctx = context.WithValue(ctx, openAIQualityContextKey{}, &openAIQualityRequest{group: group, session: "v2:cold", loaded: true})
			}
			r, err := s.SelectAccountWithLoadAwareness(ctx, &group, "v2:cold", "gpt-5.4", nil)
			results <- r
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	owner, err := cache.GetSessionAccountID(context.Background(), group, "openai:v2:cold")
	require.NoError(t, err)
	for i := 0; i < 30; i++ {
		require.NoError(t, <-errors)
		r := <-results
		require.NotNil(t, r)
		require.Equal(t, owner, r.Account.ID)
		if r.ReleaseFunc != nil {
			r.ReleaseFunc()
		}
	}
}
