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
	p := ClaudeCacheFallbackPolicy{Enabled: true, GroupIDs: []int64{7}}
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
		{"enabled", nil}, {"enabled", "true"}, {"group_ids", nil}, {"unexpected", true},
		{"group_ids", []int64{}}, {"group_ids", []int64{0}}, {"group_ids", []int64{-1}},
		{"group_ids", []int64{7, 7}}, {"group_ids", []any{7, "8"}}, {"group_ids", []any{1.5}},
		{"rules", []any{}},
	} {
		t.Run(tt.path+fmt.Sprint(tt.value), func(t *testing.T) {
			bad, e := sjson.SetBytes(raw, tt.path, tt.value)
			require.NoError(t, e)
			_, e = ParseClaudeCacheFallbackPolicy(bad)
			require.Error(t, e)
			require.NotContains(t, e.Error(), "password")
		})
	}

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
		{name: "key", mutate: func(_ *GatewayService, k *APIKey, _ *Account, _ *ParsedRequest) { k.ID = 90 }, reason: "injected"},
		{name: "account", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.ID = 99 }, reason: "injected"},
		{name: "address_path", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) {
			a.Credentials["base_url"] = "https://api.anthropic.com/other"
		}, reason: "injected"},
		{name: "mapping", mutate: func(_ *GatewayService, _ *APIKey, _ *Account, p *ParsedRequest) { p.Model = "unverified" }, reason: "injected"},
		{name: "group_fallback", mutate: func(_ *GatewayService, _ *APIKey, a *Account, p *ParsedRequest) {
			gid := int64(8)
			p.GroupID = &gid
			a.GroupIDs = []int64{7, 8}
		}, reason: "scope_mismatch"},
		{name: "oauth", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Type = AccountTypeOAuth }, reason: "injected"},
		{name: "setup", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Type = AccountTypeSetupToken }, reason: "injected"},
		{name: "vertex", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Type = AccountTypeServiceAccount }, reason: "injected"},
		{name: "other_platform", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) { a.Platform = PlatformAntigravity }, reason: "unsupported_route"},
		{name: "passthrough", mutate: func(_ *GatewayService, _ *APIKey, a *Account, _ *ParsedRequest) {
			a.Extra = map[string]any{"anthropic_passthrough": true}
		}, reason: "injected"},
		{name: "original_null_removed", original: `{"cache_control":null}`, reason: "client_declared"},
		{name: "original_thinking_removed", original: `{"messages":[{"content":[{"type":"thinking","cache_control":null}]}]}`, reason: "client_declared"},
		{name: "final_system", final: `{"system":[{"type":"text","text":"group","cache_control":{"type":"ephemeral"}}]}`, reason: "no_cacheable_content"},
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
				require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
				require.False(t, gjson.GetBytes(out, "cache_control").Exists())
				require.Equal(t, final, withoutClaudeCacheFallback(ctx, out))
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
			require.Equal(t, "ephemeral", gjson.GetBytes(upstream.lastBody, "messages.0.content.0.cache_control.type").String())
			require.False(t, HasClaudeCacheControl(parsed.Body.Bytes()))
			require.Equal(t, 2, result.Usage.InputTokens)
			require.Equal(t, 200, result.Usage.CacheCreationInputTokens)
			require.Equal(t, 9000, result.Usage.CacheReadInputTokens)
			require.Equal(t, result.Usage, result.ClaudeCacheFallback.RawUsage)
			// A new attempt with another account cannot inherit A's wire-body insertion.
			a2 := *a
			a2.ID = 12
			a2.GroupIDs = []int64{8}
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
	svc, key, _ := cacheFixture(t)
	p := svc.settingService.claudeCachePolicy(context.Background()).policy
	raw, e := json.Marshal(p)
	require.NoError(t, e)
	repo := &cacheConcurrentRepo{raw: string(raw)}
	settings := NewSettingService(repo, nil)
	settings.SetClaudeCacheGroupRepository(cacheGroupReader{group: key.Group})
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
	require.NoError(t, settings.writeSettingsWithClaudeCache(context.Background(), map[string]string{SettingKeyClaudeCacheFallbackPolicy: `{"enabled":false,"group_ids":[]}`}))
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
	svc, key, _ := cacheFixture(t)
	settings := svc.settingService
	settings.SetClaudeCacheGroupRepository(cacheGroupReader{group: key.Group})
	p := settings.claudeCachePolicy(context.Background()).policy
	require.NoError(t, settings.validateClaudeCacheScope(context.Background(), p))
	// Only group existence is required, not individual accounts, keys or models.
	key.Group.Platform = PlatformComposite
	require.NoError(t, settings.validateClaudeCacheScope(context.Background(), p))
	settings.SetClaudeCacheGroupRepository(cacheGroupReader{})
	require.Error(t, settings.validateClaudeCacheScope(context.Background(), p))
	p.Enabled = false
	require.NoError(t, settings.validateClaudeCacheScope(context.Background(), p), "disable survives deleted groups")
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
			require.Equal(t, "admin\n\nclient", gjson.GetBytes(wire, "system.0.text").String())
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

func TestClaudeCacheLegacyMigrationIsDisabled(t *testing.T) {
	legacy := []byte(`{"enabled":true,"rules":[{"id":"pilot","group_id":8,"account_id":11,"api_key_ids":[9],"models":["one-model"]},{"group_id":7},{"group_id":8}]}`)
	require.Equal(t, ClaudeCacheFallbackPolicy{GroupIDs: []int64{7, 8}}, readClaudeCacheFallbackPolicy(legacy))
	_, err := ParseClaudeCacheFallbackPolicy(legacy)
	require.Error(t, err, "old writes cannot silently expand scope")
	require.Equal(t, emptyClaudeCachePolicy(), readClaudeCacheFallbackPolicy([]byte(`{"enabled":true,"group_ids":null,"rules":[{"group_id":7}]}`)))
	svc, _, _ := cacheFixture(t)
	svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{SettingKeyClaudeCacheFallbackPolicy: string(legacy)}}, nil)
	require.False(t, svc.settingService.claudeCachePolicy(context.Background()).policy.Enabled)
}

func TestClaudeCacheBreakpoints(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		want, absent []string
	}{
		{"prefixes_and_conversation", `{"tools":[{"name":"lookup","input_schema":{"properties":{"cache_control":{}}}}],"system":"rules","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"one"},{"role":"user","content":"second"},{"role":"assistant","content":"two"},{"role":"user","content":"third"}]}`, []string{"tools.0", "system.0", "messages.2.content.0", "messages.4.content.0"}, []string{"messages.0.content.0"}},
		{"thinking_and_deferred", `{"tools":[{"name":"ready"},{"name":"deferred","defer_loading":true}],"system":"","messages":[{"role":"assistant","content":[{"type":"text","text":"answer"},{"type":"thinking","thinking":"private"},{"type":"redacted_thinking","data":"opaque"}]}]}`, []string{"tools.0", "messages.0.content.0"}, []string{"tools.1", "messages.0.content.1", "messages.0.content.2", "system"}},
		{"tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"OK"}]}]}`, []string{"messages.0.content.0"}, nil},
		{"empty_or_unknown", `{"messages":[{"role":"user","content":[{"type":"text","text":""},{"type":"future","data":"opaque"}]}]}`, nil, []string{"messages.0.content.0", "messages.0.content.1"}},
		{"gateway_hour_before_tail", `{"tools":[{"name":"lookup"}],"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hello"}]}`, []string{"system.0", "messages.0.content.0"}, []string{"tools.0"}},
		{"gateway_hour_in_later_message", `{"tools":[{"name":"lookup"}],"system":"system","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"one"},{"role":"user","content":[{"type":"text","text":"second","cache_control":{"type":"ephemeral","ttl":"1h"}}]},{"role":"assistant","content":"two"},{"role":"user","content":"third"}]}`, []string{"messages.2.content.0", "messages.4.content.0"}, []string{"tools.0", "system.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			out := addClaudeCacheFallbackBreakpoints(body)
			require.Equal(t, tc.body, string(body))
			require.False(t, gjson.GetBytes(out, "cache_control").Exists())
			for _, p := range tc.want {
				require.Equal(t, "ephemeral", gjson.GetBytes(out, p+".cache_control.type").String(), p)
			}
			for _, p := range tc.absent {
				require.False(t, gjson.GetBytes(out, p+".cache_control").Exists(), p)
			}
			require.Equal(t, out, addClaudeCacheFallbackBreakpoints(out), "idempotent insertion")
			require.True(t, claudeCacheTTLOrderValid(out))
			_, messages, tools, system := collectCacheControlPaths(out)
			require.LessOrEqual(t, len(messages)+len(tools)+len(system), 4)
			if strings.Contains(tc.name, "hour") {
				require.Contains(t, string(out), `"ttl":"1h"`)
			}
		})
	}
}

func TestClaudeCacheGatewayMarkersConsumeBudget(t *testing.T) {
	for n := 3; n <= 5; n++ {
		blocks := make([]map[string]any, n)
		for i := range blocks {
			blocks[i] = map[string]any{"type": "text", "text": "system", "cache_control": map[string]string{"type": "ephemeral"}}
		}
		body, err := json.Marshal(map[string]any{"system": blocks, "tools": []any{map[string]string{"name": "lookup"}}, "messages": []any{map[string]string{"role": "user", "content": "tail"}}})
		require.NoError(t, err)
		out := addClaudeCacheFallbackBreakpoints(body)
		if n == 3 {
			require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
			require.False(t, gjson.GetBytes(out, "tools.0.cache_control").Exists())
		} else {
			require.Equal(t, body, out, "do not replace preexisting markers")
		}
		require.JSONEq(t, gjson.GetBytes(body, "system").Raw, gjson.GetBytes(out, "system").Raw)
	}
}

func TestClaudeCacheConvertedProtocolDeclarations(t *testing.T) {
	for _, raw := range []string{
		`{"tools":[{"type":"function","function":{"name":"tool","cache_control":null}}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_text","text":"hi","cache_control":null}]}]}`,
		`{"input":[{"type":"message","role":"user","content":"hi","cache_control":null}]}`,
	} {
		require.True(t, HasClaudeCacheControl([]byte(raw)), raw)
	}
	require.False(t, HasClaudeCacheControl([]byte(`{"input":[{"type":"function_call_output","output":{"cache_control":null}}]}`)))
}

func TestClaudeCacheRemovalAtSend(t *testing.T) {
	svc, key, a := cacheFixture(t)
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, body)
	svc.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: ClaudeCacheFallbackPolicy{Enabled: true, GroupIDs: []int64{8}}, expiresAt: time.Now().Add(time.Minute)})
	actx, d := svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
	require.Equal(t, body, svc.applyClaudeCacheFallback(actx, a, body, "arbitrary"))
	require.Equal(t, "scope_mismatch", d.Reason)
}

func TestClaudeCacheAllAnthropicBuilders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"apikey", "oauth", "setup-token", "passthrough", "vertex", "bedrock-bearer", "bedrock-sigv4"} {
		t.Run(route, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			body := []byte(`{"model":"any-model","max_tokens":8,"system":"stable","messages":[{"role":"user","content":"hello"}]}`)
			switch route {
			case "oauth":
				a.Type = AccountTypeOAuth
			case "setup-token":
				a.Type = AccountTypeSetupToken
			case "vertex":
				a.Type = AccountTypeServiceAccount
				a.Credentials = map[string]any{"project_id": "fixture", "location": "us-east5"}
			case "bedrock-bearer", "bedrock-sigv4":
				a.Type = AccountTypeBedrock
			}
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, body)
			ctx, d := svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			var req *http.Request
			var err error
			switch route {
			case "passthrough":
				req, _, err = svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, a, body, "fixture")
			case "bedrock-bearer":
				req, err = svc.buildUpstreamRequestBedrockAPIKey(ctx, body, "any-model", "us-east-1", false, "fixture")
			case "bedrock-sigv4":
				req, err = svc.buildUpstreamRequestBedrock(ctx, body, "any-model", "us-east-1", false, NewBedrockSigner("fixture-id", "fixture-secret", "", "us-east-1"))
			default:
				tokenType := "apikey"
				if a.IsOAuth() {
					tokenType = "oauth"
				}
				req, _, err = svc.buildUpstreamRequest(ctx, c, a, body, "fixture", tokenType, "any-model", false, false)
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, req.Body.Close()) })
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "ephemeral", gjson.GetBytes(wire, "messages.0.content.0.cache_control.type").String())
			require.Equal(t, "ephemeral", gjson.GetBytes(wire, "system.0.cache_control.type").String())
			require.False(t, gjson.GetBytes(wire, "cache_control").Exists())
			require.Equal(t, "injected", d.Reason)
			require.False(t, HasClaudeCacheControl(body))
			if route == "bedrock-sigv4" {
				require.Contains(t, req.Header.Get("Authorization"), "AWS4-HMAC-SHA256")
			}
		})
	}
}

func TestClaudeCacheConvertedForwardWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"chat", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", route, stream), func(t *testing.T) {
				svc, key, a := cacheFixture(t)
				var raw string
				if route == "chat" {
					raw = fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream)
				} else {
					raw = fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%t,"input":"hello"}`, stream)
				}
				sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"model\":\"claude-sonnet-4-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":2,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":9000,\"cache_creation\":{\"ephemeral_5m_input_tokens\":200,\"ephemeral_1h_input_tokens\":0}}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
				svc.httpUpstream = upstream
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route, nil)
				ctx := svc.WithClaudeCacheFallbackRequest(c.Request.Context(), key, []byte(raw))
				c.Request = c.Request.WithContext(ctx)
				parsed := &ParsedRequest{GroupID: key.GroupID}
				var result *ForwardResult
				var err error
				if route == "chat" {
					result, err = svc.ForwardAsChatCompletions(ctx, c, a, []byte(raw), parsed)
				} else {
					result, err = svc.ForwardAsResponses(ctx, c, a, []byte(raw), parsed)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "injected", result.ClaudeCacheFallback.Reason)
				require.Equal(t, "ephemeral", gjson.GetBytes(upstream.lastBody, "messages.0.content.0.cache_control.type").String())
				require.Equal(t, 200, result.Usage.CacheCreationInputTokens)
				require.Equal(t, 9000, result.Usage.CacheReadInputTokens)
				require.Equal(t, result.Usage, result.ClaudeCacheFallback.RawUsage)
			})
		}
	}
}

func TestClaudeCacheRawUsageBeforeOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, oauth := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("oauth=%t/stream=%t", oauth, stream), func(t *testing.T) {
				svc, key, a := cacheFixture(t)
				gatewayForwardingSF.Forget("gateway_forwarding")
				gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
				t.Cleanup(func() {
					gatewayForwardingSF.Forget("gateway_forwarding")
					gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
				})
				if oauth {
					a.Type = AccountTypeOAuth
					a.Credentials["access_token"] = "fixture"
					repo, ok := svc.settingService.settingRepo.(*gatewayTTLSettingRepo)
					require.True(t, ok)
					require.NoError(t, repo.Set(context.Background(), SettingKeyEnableAnthropicCacheTTL1hInjection, "true"))
				} else {
					a.Type = AccountTypeSetupToken
					a.Credentials["access_token"] = "fixture"
					a.Extra = map[string]any{"cache_ttl_override_enabled": true, "cache_ttl_override_target": "5m"}
				}
				raw := fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%t,"max_tokens":8,"system":"stable","tools":[{"name":"lookup","description":"Find a fixture","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":"hello"}]}`, stream)
				response := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":5,"cache_creation_input_tokens":200,"cache_read_input_tokens":9000,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":150}}}`
				contentType := "application/json"
				if stream {
					contentType = "text/event-stream"
					response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + response + "}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5,\"input_tokens\":0,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
				svc.httpUpstream = upstream
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				ctx := svc.WithClaudeCacheFallbackRequest(c.Request.Context(), key, []byte(raw))
				c.Request = c.Request.WithContext(ctx)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(raw)), PlatformAnthropic)
				require.NoError(t, err)
				parsed.GroupID = key.GroupID
				result, err := svc.Forward(ctx, c, a, parsed)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "injected", result.ClaudeCacheFallback.Reason)
				require.Equal(t, 50, result.ClaudeCacheFallback.RawUsage.CacheCreation5mTokens)
				require.Equal(t, 150, result.ClaudeCacheFallback.RawUsage.CacheCreation1hTokens)
				require.Equal(t, 200, result.Usage.CacheCreation5mTokens)
				require.Zero(t, result.Usage.CacheCreation1hTokens)
				require.Equal(t, 9000, result.ClaudeCacheFallback.RawUsage.CacheReadInputTokens)
				tail := len(gjson.GetBytes(upstream.lastBody, "messages").Array()) - 1
				require.GreaterOrEqual(t, tail, 0)
				require.Equal(t, "ephemeral", gjson.GetBytes(upstream.lastBody, fmt.Sprintf("messages.%d.content.0.cache_control.type", tail)).String())
				require.False(t, gjson.GetBytes(parsed.Body.Bytes(), fmt.Sprintf("messages.%d.content.0.cache_control", tail)).Exists())
				require.True(t, claudeCacheTTLOrderValid(upstream.lastBody))
				if oauth {
					require.Contains(t, string(upstream.lastBody), `"ttl":"1h"`, "existing OAuth checkpoints retain configured TTL")
				}
			})
		}
	}
}
