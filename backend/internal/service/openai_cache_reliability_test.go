package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

func TestOpenAICachePreferenceRequiresRecentSamples(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{}}
	s.cfg.Gateway.OpenAICache = config.OpenAICacheConfig{AwareRoutingEnabled: true, RolloutPercent: 100}
	d := &OpenAICacheDiagnostic{Session: "v2:recent", Prefix: "prefix", WirePrefix: "prefix", Model: "gpt-5.4", GroupID: 10}
	ctx := context.WithValue(context.Background(), openAICacheDiagnosticKey{}, d)
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	r := &OpenAIForwardResult{CacheDiagnostic: d, Usage: OpenAIUsage{InputTokens: 2048, CacheReadInputTokens: 1536, CacheReadSource: "cache_read_tokens"}}
	now := time.Now()
	s.observeCacheUsage(r, a, now.Add(-43*time.Minute))
	for i := 0; i < 20; i++ {
		s.observeCacheUsage(r, a, now.Add(time.Duration(-41+2*i)*time.Minute))
	}
	_, known := s.cachePreference(ctx, a, "gpt-5.4")
	require.False(t, known, "continuous low-frequency traffic must not count expired samples")
	for i := 0; i < 5; i++ {
		s.observeCacheUsage(r, a, now.Add(-time.Second+time.Duration(i)*time.Millisecond))
	}
	_, known = s.cachePreference(ctx, a, "gpt-5.4")
	require.False(t, known, "nineteen recent samples must not enable a preference")
	s.observeCacheUsage(r, a, now.Add(-time.Second+5*time.Millisecond))
	_, known = s.cachePreference(ctx, a, "gpt-5.4")
	require.True(t, known, "twenty recent comparable samples must enable a preference")
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
	sample := openAICacheSample{count: 20, input: 2048, at: time.Now()}
	for i := range sample.recent {
		sample.recent[i] = sample.at
	}
	telemetry.scores[cacheSampleKey(d, a, "gpt-5.4")] = sample
	sample.read = 1800
	telemetry.scores[cacheSampleKey(d, b, "gpt-5.4")] = sample
	sample.read = 2000
	telemetry.scores[cacheSampleKey(d, c, "gpt-5.4")] = sample
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

// sessionUUIDWire includes the exact body bytes and every application-supplied
// header. The frozen digests were captured with e369fd3bb's three forwarding
// files through Go's overlay, independently of the new feature implementation.
type sessionUUIDWire struct {
	Method  string
	URL     string
	Host    string
	Headers http.Header
	Body    []byte
	Length  int64
}

type sessionUUIDCase struct {
	name, protocol, extra, header, baseURL, quality, variant, mapped string
	responseShape, override, pinned, success                         bool
}

func sessionUUIDBody(tc sessionUUIDCase) []byte {
	if tc.variant == "model" {
		return []byte(`{"model":"gpt-4o","input":"hello"}`)
	}
	if tc.variant == "appended" {
		return []byte(`{ "model":"gpt-6-astra", "input":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}], "stream":false, "large":9007199254740993 }`)
	}
	switch tc.protocol {
	case "chat":
		if !tc.responseShape {
			return []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"hello"}],"stream":false` + tc.extra + `}`)
		}
	case "messages":
		return []byte(`{"model":"gpt-6-astra","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false` + tc.extra + `}`)
	}
	return []byte(`{ "model":"gpt-6-astra", "input":[{"role":"user","content":"hello"}], "stream":false, "large":9007199254740993` + tc.extra + ` }`)
}

func sessionUUIDForward(t *testing.T, tc sessionUUIDCase, enabled bool, keyID int64) (sessionUUIDWire, *OpenAIGatewayService, *gin.Context, *Account) {
	t.Helper()
	resetGatewayForwardingSettingsCacheForTest(t)
	body := sessionUUIDBody(tc)
	path := "/v1/responses"
	if tc.variant == "compact" {
		path = "/v1/responses/compact"
	}
	if tc.protocol == "chat" {
		path = "/v1/chat/completions"
	}
	if tc.protocol == "messages" {
		path = "/v1/messages"
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if tc.header != "" {
		c.Request.Header.Set(tc.header, "client-session")
	}
	group := int64(10)
	c.Set("api_key", &APIKey{ID: keyID, GroupID: &group})
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "uuid-test-secret"}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"fixture stops at wire"}}`)),
	}}
	if tc.success {
		upstream.resp = openAICompatSSECompletedResponse("resp_uuid_success", "gpt-6-astra")
	}
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	if !enabled {
		s.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
			SettingKeyEnableOpenAISessionUUID: "false",
		}}, cfg)
	}
	account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-fixture"}, Extra: map[string]any{"openai_responses_supported": true}}
	switch tc.variant {
	case "passthrough":
		account.Extra["openai_passthrough"] = true
	case "grok":
		account.Platform = PlatformGrok
		account.Credentials["base_url"] = "https://api.x.ai/v1"
	case "oauth":
		account.Type = AccountTypeOAuth
		account.Credentials["access_token"] = "oauth-fixture"
		account.Credentials["chatgpt_account_id"] = "fixture-account"
		account.Extra["codex_fingerprint_mode"] = "off"
	}
	if tc.mapped != "" {
		account.Credentials["model_mapping"] = map[string]any{gjson.GetBytes(body, "model").String(): tc.mapped}
	}
	if tc.baseURL != "" {
		account.Credentials["base_url"] = tc.baseURL
	}
	if tc.override {
		account.Credentials[credKeyHeaderOverrideEnabled] = true
		account.Credentials[credKeyHeaderOverrides] = map[string]any{"x-opencode-session": "fixed-override", "x-fixture": "account-override"}
	}
	if tc.quality != "" {
		c.Request = c.Request.WithContext(withOpenAIQualityRouting(c.Request.Context()))
	}
	s.GenerateSessionHash(c, body)
	if tc.quality != "" {
		q := qualityRequest(c.Request.Context())
		require.NotNil(t, q)
		q.state = OpenAIQualityState{Generation: 7, Rotation: "quality-rotation", Binding: account.ID,
			Avoid: map[string]OpenAIQualityAvoid{"2": {Provider: qualityProvider(account)}}}
		q.loaded = true
		q.pinned = tc.pinned
		cfg.Gateway.OpenAIQualityRouting.Mode = tc.quality
	}
	key := s.ExtractSessionID(c, body)
	switch tc.protocol {
	case "chat":
		_, _ = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, key, "")
	case "messages":
		_, _ = s.ForwardAsAnthropic(c.Request.Context(), c, account, body, key, "")
	default:
		_, _ = s.Forward(c.Request.Context(), c, account, body)
	}
	wire := sessionUUIDWire{}
	if req := upstream.lastReq; req != nil {
		wire = sessionUUIDWire{Method: req.Method, URL: req.URL.String(), Host: req.Host,
			Headers: req.Header.Clone(), Body: append([]byte(nil), upstream.lastBody...), Length: req.ContentLength}
	}
	return wire, s, c, account
}

func sessionUUIDBaselineCases() []sessionUUIDCase {
	var cases []sessionUUIDCase
	for _, protocol := range []string{"chat", "messages", "responses"} {
		cases = append(cases, sessionUUIDCase{name: protocol + "/auto", protocol: protocol})
		for _, pck := range []string{`"client-key"`, `""`, `"  "`, `null`, `42`, `{"x":1}`} {
			cases = append(cases, sessionUUIDCase{name: protocol + "/pck=" + pck, protocol: protocol, extra: `,"prompt_cache_key":` + pck})
		}
		for _, header := range explicitOpenAIHeaderSessionNames {
			cases = append(cases, sessionUUIDCase{name: protocol + "/header=" + header, protocol: protocol, header: header})
			cases = append(cases, sessionUUIDCase{name: protocol + "/conflict=" + header, protocol: protocol, header: header, extra: `,"prompt_cache_key":"different-body-key"`})
		}
		for _, quality := range []string{"enforce", "observe", "off"} {
			cases = append(cases, sessionUUIDCase{name: protocol + "/quality=" + quality, protocol: protocol, quality: quality})
		}
		cases = append(cases, sessionUUIDCase{name: protocol + "/pinned", protocol: protocol, quality: "enforce", pinned: true,
			extra: `,"prompt_cache_key":"pinned-key","previous_response_id":"resp_owned"`})
		for _, base := range []string{"https://relay.example/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen/go/v1"} {
			for _, override := range []bool{false, true} {
				// Native /zen/go invents a random ID without a caller identity;
				// a fixed override gives the baseline a deterministic fallback.
				if protocol == "responses" && base == "https://opencode.ai/zen/go/v1" && !override {
					continue
				}
				cases = append(cases, sessionUUIDCase{name: fmt.Sprintf("%s/base=%s/override=%t", protocol, base, override), protocol: protocol, baseURL: base, override: override})
			}
		}
		for _, header := range explicitOpenAIHeaderSessionNames {
			cases = append(cases, sessionUUIDCase{name: protocol + "/opencode-header=" + header, protocol: protocol, baseURL: "https://opencode.ai/zen/v1", header: header, override: true})
		}
		if protocol == "messages" {
			cases = append(cases, sessionUUIDCase{name: "messages/cache-control", protocol: protocol, extra: `,"system":[{"type":"text","text":"stable project","cache_control":{"type":"ephemeral"}}]`})
			cases = append(cases, sessionUUIDCase{name: "messages/metadata", protocol: protocol, extra: `,"metadata":{"user_id":"{\"device_id\":\"device\",\"account_uuid\":\"account\",\"session_id\":\"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\"}"}`})
			cases = append(cases, sessionUUIDCase{name: "messages/opencode-metadata", protocol: protocol, baseURL: "https://opencode.ai/zen/v1", override: true, extra: `,"metadata":{"user_id":"{\"device_id\":\"device\",\"account_uuid\":\"account\",\"session_id\":\"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\"}"}`})
		}
	}
	for _, pck := range []string{"", `,"prompt_cache_key":"client-key"`, `,"prompt_cache_key":""`, `,"prompt_cache_key":null`, `,"prompt_cache_key":42`} {
		cases = append(cases, sessionUUIDCase{name: "chat/response-shape" + pck, protocol: "chat", responseShape: true, extra: pck})
	}
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, variant := range []string{"oauth", "grok", "passthrough", "compact", "model"} {
			if protocol != "responses" && variant != "oauth" && variant != "grok" {
				continue
			}
			cases = append(cases, sessionUUIDCase{name: protocol + "/excluded=" + variant, protocol: protocol, variant: variant})
		}
	}
	return cases
}

func sessionUUIDWireDigest(t *testing.T, wire sessionUUIDWire) string {
	t.Helper()
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func TestOpenAICacheSessionUUIDDisabledWireBaseline(t *testing.T) {
	raw, err := os.ReadFile("testdata/openai_session_uuid_baseline.json")
	require.NoError(t, err)
	var want map[string]string
	require.NoError(t, json.Unmarshal(raw, &want))
	for _, tc := range sessionUUIDBaselineCases() {
		t.Run(tc.name, func(t *testing.T) {
			wire, _, _, _ := sessionUUIDForward(t, tc, false, 99)
			if tc.protocol == "chat" && (strings.HasSuffix(tc.extra, ":42") || strings.HasSuffix(tc.extra, ":{\"x\":1}")) {
				require.Empty(t, wire.Method, "baseline rejects nonstring typed Chat fields before dispatch")
			} else {
				require.NotEmpty(t, wire.Method)
			}
			require.Equal(t, want[tc.name], sessionUUIDWireDigest(t, wire), "%+v", wire)
			// Explicit client input, pinned continuations and Responses-shaped
			// Chat traffic retain the independently frozen wire with B1 on too.
			if tc.header != "" || strings.Contains(tc.extra, "prompt_cache_key") || tc.responseShape || tc.variant != "" {
				on, _, _, _ := sessionUUIDForward(t, tc, true, 99)
				require.Equal(t, want[tc.name], sessionUUIDWireDigest(t, on), "%+v", on)
			}
		})
	}
}

func TestOpenAICacheSessionUUIDCompatAutomatic(t *testing.T) {
	for _, tc := range sessionUUIDBaselineCases() {
		if tc.protocol == "responses" || tc.header != "" || strings.Contains(tc.extra, "prompt_cache_key") || tc.responseShape || tc.variant != "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			off, _, _, _ := sessionUUIDForward(t, tc, false, 99)
			on, _, c, _ := sessionUUIDForward(t, tc, true, 99)
			require.NotEmpty(t, on.Method)
			require.Equal(t, off.Headers.Get("session_id"), on.Headers.Get("session_id"))
			require.Equal(t, off.Headers.Get("conversation_id"), on.Headers.Get("conversation_id"))
			key := gjson.GetBytes(on.Body, "prompt_cache_key").String()
			if tc.quality == "enforce" {
				require.Equal(t, "quality-rotation", key)
				require.Equal(t, off, on, "IQ keeps its independently selected rotation")
			} else {
				_, err := uuid.Parse(key)
				require.NoError(t, err)
				require.Equal(t, on.Headers.Get("session_id"), key)
				expected, err := sjson.SetBytes(off.Body, "prompt_cache_key", key)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(on.Body))
				headers := off.Headers.Clone()
				if strings.Contains(tc.baseURL, "opencode.ai") && !strings.Contains(tc.name, "metadata") {
					headers.Set("X-OpenCode-Session", key)
				}
				require.Equal(t, headers, on.Headers, "all final headers, including override precedence")
			}
			if tc.quality != "" {
				q := qualityRequest(c.Request.Context())
				require.Equal(t, uint64(7), q.state.Generation)
				require.Equal(t, "quality-rotation", q.state.Rotation)
			}

		})
	}
	for _, protocol := range []string{"chat", "messages"} {
		t.Run(protocol+"/tenant-scope", func(t *testing.T) {
			tc := sessionUUIDCase{protocol: protocol}
			first, _, _, _ := sessionUUIDForward(t, tc, true, 99)
			again, _, _, _ := sessionUUIDForward(t, tc, true, 99)
			other, _, _, _ := sessionUUIDForward(t, tc, true, 100)
			require.Equal(t, first, again)
			require.NotEqual(t, gjson.GetBytes(first.Body, "prompt_cache_key").String(), gjson.GetBytes(other.Body, "prompt_cache_key").String())
		})
	}
}

func TestOpenAICacheSessionUUIDResponsesWire(t *testing.T) {
	for _, base := range []string{"", "https://relay.example/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen/go/v1"} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/override=%t", base, override), func(t *testing.T) {
				tc := sessionUUIDCase{protocol: "responses", baseURL: base, override: override}
				on, _, c, _ := sessionUUIDForward(t, tc, true, 99)
				again, _, _, _ := sessionUUIDForward(t, tc, true, 99)
				require.Equal(t, on, again)
				key := gjson.GetBytes(on.Body, "prompt_cache_key").String()
				_, err := uuid.Parse(key)
				require.NoError(t, err)
				require.Equal(t, generateSessionUUID("xy2api:openai-filled-pck:v1:"+c.GetString(openAICacheSessionGinKey)), key)
				other, _, _, _ := sessionUUIDForward(t, tc, true, 100)
				require.NotEqual(t, key, gjson.GetBytes(other.Body, "prompt_cache_key").String())
				off, _, _, _ := sessionUUIDForward(t, tc, false, 99)
				expected, err := sjson.SetBytes(off.Body, "prompt_cache_key", key)
				require.NoError(t, err)
				require.Equal(t, expected, on.Body, "single-field patch retains large numbers and other raw bytes")
				require.Equal(t, "9007199254740993", gjson.GetBytes(on.Body, "large").Raw)
				headers := off.Headers.Clone()
				if strings.Contains(base, "opencode.ai") {
					headers.Set("X-OpenCode-Session", key)
				}
				require.Equal(t, headers, on.Headers)
				require.Equal(t, off.URL, on.URL)
				require.Equal(t, off.Host, on.Host)
				require.Equal(t, int64(len(on.Body)), on.Length)
			})
		}
	}
}

func TestOpenAICacheSessionUUIDResponsesExclusions(t *testing.T) {
	for _, name := range []string{"eligible", "nil-config", "empty-secret", "zero-key", "oauth", "grok", "passthrough", "compact", "compact-child", "ws-ingress", "unknown-ingress", "ws-upstream", "model", "missing-v2", "empty-v2", "pck-empty", "pck-null", "pck-number", "explicit-header", "disabled"} {
		t.Run(name, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "uuid-test-secret"}}}
			c := cacheTestContext(99, 10)
			c.Request.Header.Del("session-id")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			body := []byte(`{"model":"gpt-6-astra","input":"hello"}`)
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			transport := OpenAIUpstreamTransportHTTPSSE
			model := "gpt-6-astra"
			switch name {
			case "nil-config":
				s.cfg = nil
			case "empty-secret":
				s.cfg.JWT.Secret = ""
			case "zero-key":
				c.Set("api_key", &APIKey{})
			case "oauth":
				account.Type = AccountTypeOAuth
			case "grok":
				account.Platform = PlatformGrok
			case "passthrough":
				account.Extra = map[string]any{"openai_passthrough": true}
			case "compact":
				c.Request.URL.Path = "/v1/responses/compact"
			case "compact-child":
				c.Request.URL.Path = "/v1/responses/compact/child"
			case "ws-ingress":
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
			case "unknown-ingress":
				c.Set(openAIClientTransportContextKey, OpenAIClientTransportUnknown)
			case "ws-upstream":
				transport = OpenAIUpstreamTransportResponsesWebsocketV2
			case "model":
				model = "gpt-4o"
			case "pck-empty":
				body = []byte(`{"model":"gpt-6-astra","prompt_cache_key":"","input":"hello"}`)
			case "pck-null":
				body = []byte(`{"model":"gpt-6-astra","prompt_cache_key":null,"input":"hello"}`)
			case "pck-number":
				body = []byte(`{"model":"gpt-6-astra","prompt_cache_key":42,"input":"hello"}`)
			case "explicit-header":
				c.Request.Header.Set("X-Session-Id", "client")
			case "disabled":
				s.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{SettingKeyEnableOpenAISessionUUID: "false"}}, s.cfg)
			}
			// These three configuration cases exercise the real fallback seed
			// writer, which can still emit v2 without a persistent JWT secret.
			s.GenerateSessionHash(c, body)
			if name == "nil-config" || name == "empty-secret" {
				require.True(t, strings.HasPrefix(c.GetString(openAICacheSessionGinKey), "v2:"))
			}
			if name == "missing-v2" {
				c.Set(openAICacheSessionGinKey, "legacy")
			}
			if name == "empty-v2" {
				c.Set(openAICacheSessionGinKey, "v2:")
			}
			key := s.fillOpenAIResponsesPromptCacheKey(c.Request.Context(), c, account, body, model, transport)
			if name == "eligible" {
				require.NotEmpty(t, key)
			} else {
				require.Empty(t, key)
			}
		})
	}
}

type sessionUUIDErrorRepo struct{ *gatewayTTLSettingRepo }

func (r *sessionUUIDErrorRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("fixture read error")
}

func TestOpenAICacheSessionUUIDSettings(t *testing.T) {
	ctx := context.Background()
	var absent *SettingService
	require.True(t, absent.IsOpenAISessionUUIDEnabled(ctx))
	require.True(t, (&SettingService{}).IsOpenAISessionUUIDEnabled(ctx))
	require.True(t, (*OpenAIGatewayService)(nil).openAISessionUUIDEnabled(ctx))
	for _, tc := range []struct {
		name, value string
		want        bool
	}{{"missing", "", true}, {"empty", "", true}, {"enabled", "true", true}, {"disabled", "false", false}} {
		t.Run(tc.name, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			repo := &gatewayTTLSettingRepo{data: map[string]string{SettingKeyEnableOpenAISessionUUID: tc.value}}
			if tc.name == "missing" {
				delete(repo.data, SettingKeyEnableOpenAISessionUUID)
			}
			s := NewSettingService(repo, &config.Config{})
			require.Equal(t, tc.want, s.parseSettings(repo.data).EnableOpenAISessionUUID)
			require.Equal(t, tc.want, s.IsOpenAISessionUUIDEnabled(ctx))
			require.Equal(t, tc.want, s.IsOpenAISessionUUIDEnabled(ctx), "cached snapshot")
			settings := s.parseSettings(repo.data)
			settings.EnableOpenAISessionUUID = !tc.want
			require.NoError(t, s.UpdateSettings(ctx, settings))
			require.Equal(t, !tc.want, s.IsOpenAISessionUUIDEnabled(ctx), "save refreshes snapshot immediately")
			require.Equal(t, fmt.Sprint(!tc.want), repo.data[SettingKeyEnableOpenAISessionUUID])
		})
	}
	t.Run("read-error", func(t *testing.T) {
		resetGatewayForwardingSettingsCacheForTest(t)
		s := NewSettingService(&sessionUUIDErrorRepo{&gatewayTTLSettingRepo{}}, &config.Config{})
		require.True(t, s.IsOpenAISessionUUIDEnabled(ctx))
		require.True(t, s.IsOpenAISessionUUIDEnabled(ctx), "error snapshot also defaults on")
	})
}

func TestOpenAICacheSessionUUIDQualityContinuation(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, pinned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pinned=%t", protocol, pinned), func(t *testing.T) {
				tc := sessionUUIDCase{protocol: protocol, quality: "enforce", pinned: pinned, success: true}
				if pinned {
					tc.extra = `,"prompt_cache_key":"pinned-key","previous_response_id":"resp_owned"`
				}
				off, _, offContext, _ := sessionUUIDForward(t, tc, false, 99)
				on, s, c, account := sessionUUIDForward(t, tc, true, 99)
				require.NotEmpty(t, on.Method)
				require.Equal(t, off, on, "IQ enforce and pinned final bytes and all headers retain baseline")
				q := qualityRequest(c.Request.Context())
				require.Equal(t, uint64(7), q.state.Generation)
				require.Equal(t, "quality-rotation", q.state.Rotation)
				require.Equal(t, qualityRequest(offContext.Request.Context()).session, q.session, "routing session identity is unchanged")
				if !pinned {
					require.Equal(t, "quality-rotation", gjson.GetBytes(on.Body, "prompt_cache_key").String())
				}
				if protocol == "messages" {
					var req apicompat.AnthropicRequest
					require.NoError(t, json.Unmarshal(sessionUUIDBody(tc), &req))
					originalKey := s.ExtractSessionID(c, sessionUUIDBody(tc))
					if originalKey == "" {
						originalKey = promptCacheKeyFromAnthropicDigest(buildOpenAICompatAnthropicDigestChain(&req))
					}
					require.Equal(t, "resp_uuid_success", s.getOpenAICompatSessionResponseID(c.Request.Context(), c, account, originalKey), "real successful forward binds the original continuation key")
					if !pinned {
						wireKey := gjson.GetBytes(on.Body, "prompt_cache_key").String()
						require.Empty(t, s.getOpenAICompatSessionResponseID(c.Request.Context(), c, account, wireKey), "rotation cannot replace the local continuation key")
						reused, _ := s.findOpenAICompatAnthropicDigestPromptCacheKey(account, 99, buildOpenAICompatAnthropicDigestChain(&req))
						require.Equal(t, originalKey, reused, "digest keeps the original key")
					}
				}
			})
		}
	}
}

func TestOpenAICacheSessionUUIDExcludedWireUnchanged(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, variant := range []string{"oauth", "grok", "passthrough", "compact", "model"} {
			if protocol != "responses" && variant != "oauth" && variant != "grok" {
				continue
			}
			t.Run(protocol+"/"+variant, func(t *testing.T) {
				tc := sessionUUIDCase{protocol: protocol, variant: variant}
				off, _, _, _ := sessionUUIDForward(t, tc, false, 99)
				on, _, _, _ := sessionUUIDForward(t, tc, true, 99)
				require.NotEmpty(t, off.Method)
				require.Equal(t, off, on, "all outgoing headers and body bytes retain baseline")
			})
		}
	}
}

func TestOpenAICacheSessionUUIDResponsesDecodedAndLaterTurn(t *testing.T) {
	tc := sessionUUIDCase{protocol: "responses", extra: `,"text":{"format":{"type":"json_schema","schema":{"type":"object","uniqueItems":true,"properties":{}}}}`}
	off, _, _, _ := sessionUUIDForward(t, tc, false, 99)
	on, _, _, _ := sessionUUIDForward(t, tc, true, 99)
	require.NotEmpty(t, on.Method)
	require.False(t, gjson.GetBytes(on.Body, "text.format.schema.uniqueItems").Exists(), "forces decoded-map rebuild")
	key := gjson.GetBytes(on.Body, "prompt_cache_key").String()
	require.NotEmpty(t, key)
	expected, err := sjson.SetBytes(off.Body, "prompt_cache_key", key)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(on.Body), "fill survives full marshal path")
	require.Equal(t, off.Headers, on.Headers)
	first, _, _, _ := sessionUUIDForward(t, sessionUUIDCase{protocol: "responses"}, true, 99)
	later, _, _, _ := sessionUUIDForward(t, sessionUUIDCase{protocol: "responses", variant: "appended"}, true, 99)
	require.Equal(t, gjson.GetBytes(first.Body, "prompt_cache_key").String(), gjson.GetBytes(later.Body, "prompt_cache_key").String(), "appended round retains stable leading identity")
}

func TestOpenAICacheSessionUUIDResponsesScopeAndModelMapping(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "uuid-test-secret"}}}
	key := func(api, group int64, model string, accountID int64) string {
		c := cacheTestContext(api, group)
		c.Request.Header.Del("session-id")
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		body := []byte(fmt.Sprintf(`{"model":%q,"input":"hello"}`, model))
		identity := s.GenerateSessionHash(c, body)
		filled := s.fillOpenAIResponsesPromptCacheKey(c.Request.Context(), c, &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, body, model, OpenAIUpstreamTransportHTTPSSE)
		require.NotEmpty(t, filled)
		require.Equal(t, identity, c.GetString(openAICacheSessionGinKey))
		return filled
	}
	first := key(99, 10, "gpt-6-astra", 2)
	require.Equal(t, first, key(99, 10, "gpt-6-astra", 2), "retry")
	require.Equal(t, first, key(99, 10, "gpt-6-astra", 3), "upstream failover")
	require.NotEqual(t, first, key(100, 10, "gpt-6-astra", 2))
	require.NotEqual(t, first, key(99, 11, "gpt-6-astra", 2))
	require.NotEqual(t, first, key(99, 10, "gpt-5.4", 2))
	for _, tc := range []sessionUUIDCase{
		{protocol: "responses", variant: "model", mapped: "gpt-6-astra"},
		{protocol: "responses", mapped: "gpt-4o"},
	} {
		t.Run(tc.mapped, func(t *testing.T) {
			wire, _, _, _ := sessionUUIDForward(t, tc, true, 99)
			require.NotEmpty(t, wire.Method)
			require.Equal(t, tc.mapped, gjson.GetBytes(wire.Body, "model").String())
			require.Equal(t, tc.mapped == "gpt-6-astra", gjson.GetBytes(wire.Body, "prompt_cache_key").Exists(), "gate follows mapped upstream model")
		})
	}
}
