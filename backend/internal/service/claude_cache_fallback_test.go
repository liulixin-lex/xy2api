package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const cacheTestModel = "claude-opus-5-5"

func cacheFixture(t testing.TB) (*GatewayService, *APIKey, *Account) {
	t.Helper()
	p := ClaudeCacheFallbackPolicy{Enabled: true, Rules: []ClaudeCacheFallbackRule{{ID: "pilot", GroupID: 7, APIKeyIDs: []int64{9}, AccountID: 11, BaseURL: "https://api.anthropic.com", Models: []string{cacheTestModel}}}}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{SettingKeyClaudeCacheFallbackPolicy: string(raw)}}, cfg)
	svc := &GatewayService{cfg: cfg, settingService: settings, rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}, responseHeaderFilter: compileResponseHeaderFilter(cfg), billingService: NewBillingService(cfg, nil)}
	gid := int64(7)
	key := &APIKey{ID: 9, GroupID: &gid, Group: &Group{ID: gid, Platform: PlatformAnthropic, RateMultiplier: 1}, User: &User{ID: 3}}
	a := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, GroupIDs: []int64{7}, Credentials: map[string]any{"api_key": "fixture-key"}, Status: StatusActive, Schedulable: true, Concurrency: 1}
	return svc, key, a
}

func TestClaudeCacheProtocolPresence(t *testing.T) {
	for _, body := range []string{
		`{"cache_control":null}`, `{"cache_control":false}`, `{"cache_control":"invalid"}`,
		`{"system":[{"type":"text","cache_control":null}]}`, `{"tools":[{"name":"tool","cache_control":{}}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"thinking","cache_control":null}]}]}`,
	} {
		require.True(t, HasClaudeCacheControl([]byte(body)), body)
	}
	for _, body := range []string{
		`{"system":"cache_control"}`, `{"tools":[{"input_schema":{"properties":{"cache_control":{}}}}]}`,
		`{"messages":[{"content":[{"type":"tool_use","input":{"cache_control":null}}]}]}`,
	} {
		require.False(t, HasClaudeCacheControl([]byte(body)), body)
	}
}

func TestClaudeCachePolicyValidation(t *testing.T) {
	svc, _, _ := cacheFixture(t)
	p := svc.settingService.claudeCachePolicy(context.Background()).policy
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	for _, tt := range []struct {
		path  string
		value any
	}{
		{"enabled", nil}, {"enabled", "true"}, {"rules", nil}, {"unexpected", true},
		{"rules.0.id", "bad\nlog"}, {"rules.0.group_id", 0}, {"rules.0.account_id", -1},
		{"rules.0.base_url", "https://secret:password@example.com"}, {"rules.0.base_url", "https://example.com?token=secret"},
		{"rules.0.models", []string{"claude-*"}}, {"rules.0.models", []string{}}, {"rules.0.api_key_ids", []int64{9, 9}},
	} {
		t.Run(tt.path+fmt.Sprint(tt.value), func(t *testing.T) {
			bad, e := sjson.SetBytes(raw, tt.path, tt.value)
			require.NoError(t, e)
			_, e = ParseClaudeCacheFallbackPolicy(bad)
			require.Error(t, e)
			require.NotContains(t, e.Error(), "password")
		})
	}
	a, e := normalizeClaudeCacheBaseURL("https://PROVIDER.example/tenant/a/")
	require.NoError(t, e)
	require.Equal(t, "https://provider.example/tenant/a", a)
	b, e := normalizeClaudeCacheBaseURL("https://provider.example/tenant/b")
	require.NoError(t, e)
	require.NotEqual(t, a, b)
}

func TestClaudeCacheScopeAndOriginalProtection(t *testing.T) {
	body := []byte(`{ "model":"claude-opus-5-5", "messages":[{"role":"user","content":"hello"}], "max_tokens":5 }`)
	cases := []struct {
		name     string
		mutate   func(*GatewayService, *APIKey, *Account, *ParsedRequest)
		original string
		final    string
		reason   string
	}{
		{name: "inject", reason: "injected"},
		{name: "off", mutate: func(s *GatewayService, _ *APIKey, _ *Account, _ *ParsedRequest) {
			s.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Minute)})
		}, reason: "disabled"},
		{name: "key", mutate: func(_ *GatewayService, k *APIKey, _ *Account, _ *ParsedRequest) { k.ID = 90 }, reason: "scope_mismatch"},
		{name: "account", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.ID = 99 }, reason: "scope_mismatch"},
		{name: "address_path", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) {
			a.Credentials["base_url"] = "https://api.anthropic.com/other"
		}, reason: "scope_mismatch"},
		{name: "mapping", mutate: func(_ *GatewayService, _ *APIKey, _ *Account, p *ParsedRequest) { p.Model = "unverified" }, reason: "scope_mismatch"},
		{name: "group_fallback", mutate: func(_ *GatewayService, _ *APIKey, a *Account, p *ParsedRequest) {
			gid := int64(8)
			p.GroupID = &gid
			a.GroupIDs = []int64{7, 8}
		}, reason: "scope_mismatch"},
		{name: "oauth", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Type = AccountTypeOAuth }, reason: "unsupported_route"},
		{name: "setup", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Type = AccountTypeSetupToken }, reason: "unsupported_route"},
		{name: "vertex", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Type = AccountTypeServiceAccount }, reason: "unsupported_route"},
		{name: "other_platform", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Platform = PlatformAntigravity }, reason: "unsupported_route"},
		{name: "passthrough", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) {
			a.Extra = map[string]any{"anthropic_passthrough": true}
		}, reason: "unsupported_route"},
		{name: "original_null_removed", original: `{"cache_control":null}`, reason: "client_declared"},
		{name: "original_thinking_removed", original: `{"messages":[{"content":[{"type":"thinking","cache_control":null}]}]}`, reason: "client_declared"},
		{name: "final_system", final: `{"system":[{"type":"text","text":"group","cache_control":{"type":"ephemeral"}}]}`, reason: "final_declared"},
		{name: "bad_json", final: `{`, reason: "invalid_body"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			parsed := &ParsedRequest{Model: cacheTestModel, GroupID: key.GroupID}
			if tt.mutate != nil {
				tt.mutate(svc, key, a, parsed)
			}
			original := body
			if tt.original != "" {
				original = []byte(tt.original)
			}
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, original)
			ctx, d := svc.beginClaudeCacheAttempt(ctx, parsed, a)
			final := body
			if tt.final != "" {
				final = []byte(tt.final)
			}
			before := string(final)
			out := svc.applyClaudeCacheFallback(ctx, a, final, parsed.Model)
			require.Equal(t, tt.reason, d.Reason)
			require.Equal(t, before, string(final), "shared input must remain immutable")
			if tt.reason == "injected" {
				require.Equal(t, "ephemeral", gjson.GetBytes(out, "cache_control.type").String())
				require.False(t, gjson.GetBytes(out, "cache_control.ttl").Exists())
				clean, e := sjson.DeleteBytes(out, "cache_control")
				require.NoError(t, e)
				require.JSONEq(t, string(body), string(clean))
				again := svc.applyClaudeCacheFallback(ctx, a, out, parsed.Model)
				require.Equal(t, out, again)
			} else {
				require.Equal(t, final, out)
			}
		})
	}
}

func TestClaudeCacheForwardWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			raw := fmt.Sprintf(`{"model":"%s","stream":%t,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, cacheTestModel, stream)
			responseBody := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":5,"cache_creation_input_tokens":200,"cache_read_input_tokens":9000,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":0}}}`
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
				responseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + responseBody + "}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5,\"input_tokens\":0,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}, "X-Request-Id": {"cache-wire"}}, Body: io.NopCloser(strings.NewReader(responseBody))}}
			svc.httpUpstream = upstream
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			ctx := svc.WithClaudeCacheFallbackRequest(c.Request.Context(), key, []byte(raw))
			c.Request = c.Request.WithContext(ctx)
			parsed, e := ParseGatewayRequest(NewRequestBodyRef([]byte(raw)), PlatformAnthropic)
			require.NoError(t, e)
			parsed.GroupID = key.GroupID
			result, e := svc.Forward(ctx, c, a, parsed)
			require.NoError(t, e)
			require.NotNil(t, result)
			require.Equal(t, "ephemeral", gjson.GetBytes(upstream.lastBody, "cache_control.type").String())
			require.False(t, HasClaudeCacheControl(parsed.Body.Bytes()))
			require.Equal(t, 2, result.Usage.InputTokens)
			require.Equal(t, 200, result.Usage.CacheCreationInputTokens)
			require.Equal(t, 9000, result.Usage.CacheReadInputTokens)
			require.Equal(t, result.Usage, result.ClaudeCacheFallback.RawUsage)
			// A new attempt with another account cannot inherit A's wire-body insertion.
			a2 := *a
			a2.ID = 12
			actx, d := svc.beginClaudeCacheAttempt(ctx, parsed, &a2)
			req, out, e := svc.buildUpstreamRequest(actx, c, &a2, []byte(raw), "fixture", "apikey", cacheTestModel, stream, false)
			require.NoError(t, e)
			require.NoError(t, req.Body.Close())
			require.Equal(t, []byte(raw), out)
			require.Equal(t, "scope_mismatch", d.Reason)
			// Calling the shared builder without native admission leaves conversion/count paths unchanged.
			req, out, e = svc.buildUpstreamRequest(context.Background(), c, a, []byte(raw), "fixture", "apikey", cacheTestModel, stream, false)
			require.NoError(t, e)
			require.NoError(t, req.Body.Close())
			require.Equal(t, []byte(raw), out)
		})
	}
}

type cacheGroupReader struct {
	GroupRepository
	group *Group
}

func (r cacheGroupReader) GetByID(context.Context, int64) (*Group, error) { return r.group, nil }

type cacheAccountReader struct {
	AccountRepository
	account *Account
}

func (r cacheAccountReader) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }

type cacheKeyReader struct {
	APIKeyRepository
	key *APIKey
}

func (r cacheKeyReader) GetByID(context.Context, int64) (*APIKey, error) { return r.key, nil }

type cacheConcurrentRepo struct {
	SettingRepository
	mu    sync.Mutex
	raw   string
	fail  bool
	reads int
}

func (r *cacheConcurrentRepo) GetValue(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.fail {
		return "", errors.New("unavailable")
	}
	return r.raw, nil
}
func (r *cacheConcurrentRepo) SetMultiple(_ context.Context, v map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("unavailable")
	}
	r.raw = v[SettingKeyClaudeCacheFallbackPolicy]
	return nil
}

func TestClaudeCacheSnapshotSaveExpiryAndIsolation(t *testing.T) {
	svc, key, a := cacheFixture(t)
	p := svc.settingService.claudeCachePolicy(context.Background()).policy
	raw, e := json.Marshal(p)
	require.NoError(t, e)
	repo := &cacheConcurrentRepo{raw: string(raw)}
	settings := NewSettingService(repo, nil)
	settings.SetClaudeCacheScopeRepositories(cacheGroupReader{group: key.Group}, cacheAccountReader{account: a}, cacheKeyReader{key: key})
	other := NewSettingService(repo, nil)
	require.True(t, settings.claudeCachePolicy(context.Background()).policy.Enabled)
	require.True(t, other.claudeCachePolicy(context.Background()).policy.Enabled)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				require.NotNil(t, settings.claudeCachePolicy(context.Background()))
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 2, repo.reads)
	require.NoError(t, settings.writeSettingsWithClaudeCache(context.Background(), map[string]string{SettingKeyClaudeCacheFallbackPolicy: `{"enabled":false,"rules":[]}`}))
	require.False(t, settings.claudeCachePolicy(context.Background()).policy.Enabled)
	require.True(t, other.claudeCachePolicy(context.Background()).policy.Enabled)
	other.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: p, expiresAt: time.Now().Add(-time.Second)})
	require.False(t, other.claudeCachePolicy(context.Background()).policy.Enabled)
	repo.mu.Lock()
	repo.fail = true
	repo.mu.Unlock()
	settings.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: p, expiresAt: time.Now().Add(-time.Second)})
	require.False(t, settings.claudeCachePolicy(context.Background()).policy.Enabled, "expired ON must not survive read failure")
	require.Error(t, settings.writeSettingsWithClaudeCache(context.Background(), map[string]string{SettingKeyClaudeCacheFallbackPolicy: string(raw)}))
	require.False(t, settings.claudeCachePolicy(context.Background()).policy.Enabled)
}

func TestClaudeCacheAdmissionAndDisable(t *testing.T) {
	svc, key, a := cacheFixture(t)
	body := []byte(`{"messages":[]}`)
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, body)
	svc.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Minute)})
	actx, d := svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
	require.Equal(t, body, svc.applyClaudeCacheFallback(actx, a, body, cacheTestModel))
	require.Equal(t, "disabled", d.Reason)
	offctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, body)
	svc.settingService.claudeCacheSnapshot.Store(nil) // reload original ON
	actx, d = svc.beginClaudeCacheAttempt(offctx, &ParsedRequest{GroupID: key.GroupID}, a)
	require.Equal(t, body, svc.applyClaudeCacheFallback(actx, a, body, cacheTestModel))
	require.Equal(t, "disabled", d.Reason)
}

func BenchmarkClaudeCacheFallbackLarge(b *testing.B) {
	svc, key, a := cacheFixture(b)
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("a", 2_650_000) + `"}]}`)
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprint(on), func(b *testing.B) {
			if !on {
				svc.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Hour)})
			} else {
				svc.settingService.claudeCacheSnapshot.Store(nil)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for range b.N {
				ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, body)
				ctx, _ = svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
				svc.applyClaudeCacheFallback(ctx, a, body, cacheTestModel)
			}
		})
	}
}

func TestClaudeCacheScopeSaveValidation(t *testing.T) {
	svc, key, a := cacheFixture(t)
	settings := svc.settingService
	settings.SetClaudeCacheScopeRepositories(cacheGroupReader{group: key.Group}, cacheAccountReader{account: a}, cacheKeyReader{key: key})
	p := settings.claudeCachePolicy(context.Background()).policy
	require.NoError(t, settings.validateClaudeCacheScope(context.Background(), p))
	a.GroupIDs = nil
	require.Error(t, settings.validateClaudeCacheScope(context.Background(), p))
	a.GroupIDs = []int64{7}
	a.Credentials["base_url"] = "https://api.anthropic.com/different"
	require.Error(t, settings.validateClaudeCacheScope(context.Background(), p))
	a.Credentials["base_url"] = "https://api.anthropic.com"
	other := int64(99)
	key.GroupID = &other
	require.Error(t, settings.validateClaudeCacheScope(context.Background(), p))
	p.Enabled = false
	require.NoError(t, settings.validateClaudeCacheScope(context.Background(), p), "emergency disable survives removed or moved resources")
}

func TestClaudeCacheGroupPromptAndConcurrentBody(t *testing.T) {
	svc, key, a := cacheFixture(t)
	body := []byte(`{"model":"claude-opus-5-5","system":"client","messages":[]}`)
	before := string(body)
	ctx := WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{Prompt: "admin"}, cacheTestModel)
	ctx = svc.WithClaudeCacheFallbackRequest(ctx, key, body)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			actx, d := svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
			req, wire, e := svc.buildUpstreamRequest(actx, nil, a, body, "fixture", "apikey", cacheTestModel, false, false)
			require.NoError(t, e)
			require.NoError(t, req.Body.Close())
			require.Equal(t, "admin\n\nclient", gjson.GetBytes(wire, "system").String())
			require.Equal(t, "injected", d.Reason)
		}()
	}
	wg.Wait()
	require.Equal(t, before, string(body))
}

type cacheCountingUpstream struct {
	*anthropicHTTPUpstreamRecorder
	calls int
}

func (u *cacheCountingUpstream) DoWithTLS(req *http.Request, proxy string, id int64, n int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	return u.anthropicHTTPUpstreamRecorder.DoWithTLS(req, proxy, id, n, profile)
}

func TestClaudeCacheForwardFailuresDoNotProbeOrReplay(t *testing.T) {
	for _, kind := range []string{"reject_field", "canceled", "partial_stream"} {
		t.Run(kind, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			stream := kind == "partial_stream"
			raw := []byte(fmt.Sprintf(`{"model":"%s","stream":%t,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, cacheTestModel, stream))
			upstream := &cacheCountingUpstream{anthropicHTTPUpstreamRecorder: &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"cache_control: extra inputs are not permitted"}}`))}}}
			if kind == "canceled" {
				upstream.err = context.Canceled
			}
			if stream {
				upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"partial\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":2,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":9000}}}\n\n"))}
			}
			svc.httpUpstream = upstream
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			ctx := svc.WithClaudeCacheFallbackRequest(c.Request.Context(), key, raw)
			c.Request = c.Request.WithContext(ctx)
			parsed, e := ParseGatewayRequest(NewRequestBodyRef(raw), PlatformAnthropic)
			require.NoError(t, e)
			parsed.GroupID = key.GroupID
			result, e := svc.Forward(ctx, c, a, parsed)
			require.Error(t, e)
			require.Equal(t, 1, upstream.calls, "cache policy cannot introduce retries or capability probes")
			require.False(t, HasClaudeCacheControl(parsed.Body.Bytes()))
			if stream {
				require.NotNil(t, result)
				require.Equal(t, 200, result.Usage.CacheCreationInputTokens)
				require.Equal(t, result.Usage, result.ClaudeCacheFallback.RawUsage)
			}
		})
	}
}
