//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type l04HandlerPolicyStore struct {
	service.SettingRepository
	enabled bool
}

func (s *l04HandlerPolicyStore) GetMultiple(context.Context, []string) (map[string]string, error) {
	master := `{"rev":1,"spec":{"enabled":false}}`
	if s.enabled {
		master = `{"rev":1,"spec":{"enabled":true}}`
	}
	return map[string]string{"cache_feature_master": master, "cache_anthropic_identity_policy": `{"rev":7,"spec":{"anchor_group_ids":[31,77],"rollout_percent":100,"gemini_anchor":true}}`}, nil
}

type l04HandlerStickyCall struct {
	operation string
	group     int64
	hash      string
	account   int64
}
type l04HandlerSticky struct {
	service.GatewayCache
	calls []l04HandlerStickyCall
}

func (s *l04HandlerSticky) GetSessionAccountID(_ context.Context, g int64, h string) (int64, error) {
	s.calls = append(s.calls, l04HandlerStickyCall{operation: "get", group: g, hash: h})
	return 0, service.ErrStickySessionNotFound
}
func (s *l04HandlerSticky) SetSessionAccountID(_ context.Context, g int64, h string, a int64, _ time.Duration) error {
	s.calls = append(s.calls, l04HandlerStickyCall{operation: "set", group: g, hash: h, account: a})
	return nil
}
func (*l04HandlerSticky) DeleteSessionAccountID(context.Context, int64, string) error { return nil }
func (*l04HandlerSticky) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

type l04HandlerUpstream struct {
	ids            []int64
	bodies         [][]byte
	promptFallback bool
}

func (u *l04HandlerUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.ids = append(u.ids, id)
	u.bodies = append(u.bodies, raw)
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"synthetic terminal error"}}`
	if u.promptFallback && id == 1 {
		body = `{"error":{"message":"Prompt is too long"}}`
	}
	return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}
func (u *l04HandlerUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

func l04HandlerFixture(t *testing.T, caps service.CacheM4BuildCapabilities, enabled, promptFallback bool) (*GatewayHandler, *service.APIKey, *l04HandlerSticky, *l04HandlerUpstream) {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "l04-handler-synthetic"
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	policies := service.NewCachePolicyService(&l04HandlerPolicyStore{enabled: enabled}, cfg, service.WithCacheM4Policies(nil), service.WithCacheM4BuildCapabilities(caps), service.WithProviderCacheTrace())
	require.NoError(t, policies.Refresh(context.Background()))
	engine := service.ProvideOpenAIGatewayServiceWithCachePolicies(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, policies, nil)
	a := &service.Group{ID: 31, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	b := &service.Group{ID: 77, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	if promptFallback {
		a.FallbackGroupIDOnInvalidRequest = &b.ID
	}
	first := &service.Account{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic"}, AccountGroups: []service.AccountGroup{{GroupID: 31, AccountID: 1}}}
	if promptFallback {
		first.Platform = service.PlatformAntigravity
		first.Type = service.AccountTypeOAuth
		first.Credentials = map[string]any{"access_token": "synthetic", "project_id": "synthetic-project", "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}
	}
	second := &service.Account{ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic"}, AccountGroups: []service.AccountGroup{{GroupID: 77, AccountID: 2}}}
	scheduler := service.NewSchedulerSnapshotService(&groupScopedSchedulerCache{fakeSchedulerCache: &fakeSchedulerCache{accounts: []*service.Account{first, second}}}, nil, nil, nil, nil)
	groups := &groupMapRepo{fakeGroupRepo: &fakeGroupRepo{}, groups: map[int64]*service.Group{31: a, 77: b}}
	cache := &l04HandlerSticky{}
	upstream := &l04HandlerUpstream{promptFallback: promptFallback}
	gateway := service.ProvideGatewayServiceWithCacheTrace(nil, groups, nil, nil, nil, nil, nil, cache, cfg, scheduler, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, &service.TLSFingerprintProfileService{}, nil, nil, nil, nil, nil, engine)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := &GatewayHandler{gatewayService: gateway, billingCacheService: billing, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0), cfg: cfg, maxAccountSwitches: 1}
	h.antigravityGatewayService = service.NewAntigravityGatewayService(nil, cache, scheduler, service.NewAntigravityTokenProvider(nil, nil, nil), nil, upstream, nil, nil)
	k := &service.APIKey{ID: 101, UserID: 102, GroupID: &a.ID, Group: a, Status: service.StatusActive, User: &service.User{ID: 102, Concurrency: 10, Balance: 100}}
	return h, k, cache, upstream
}
func l04ServeHandler(t *testing.T, h *GatewayHandler, k *service.APIKey, path, body string, call func(*GatewayHandler, *gin.Context)) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)).WithContext(context.WithValue(context.Background(), ctxkey.Group, k.Group))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), k)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: k.UserID, Concurrency: 10})
	call(h, c)
	require.Equal(t, 400, w.Code)
	current, _ := c.Get(string(middleware.ContextKeyAPIKey))
	require.Same(t, k, current, "authentication and billing key are unchanged")
	return append([]byte(nil), w.Body.Bytes()...)
}

// Antigravity's existing request transformer generates this single UUID field
// on every call. Validate its exact shape and mask only its value span; every
// other byte, including the stable upstream sessionId, must compare verbatim.
func l04PromptFallbackWireForComparison(t *testing.T, bodies [][]byte) [][]byte {
	t.Helper()
	require.Len(t, bodies, 2)
	first := bodies[0]
	id := gjson.GetBytes(first, "requestId")
	require.Equal(t, 1, bytes.Count(first, []byte(`"requestId":`)))
	require.Equal(t, gjson.String, id.Type)
	require.True(t, strings.HasPrefix(id.String(), "agent-"))
	parsed, err := uuid.Parse(strings.TrimPrefix(id.String(), "agent-"))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), parsed.Version())
	fixed := []byte(`"agent-00000000-0000-4000-8000-000000000000"`)
	normalized := append([]byte(nil), first[:id.Index]...)
	normalized = append(normalized, fixed...)
	normalized = append(normalized, first[id.Index+len(id.Raw):]...)
	return [][]byte{normalized, bodies[1]}
}

func TestL04PromptTooLongActualFallbackOffOnBytesAndSequence(t *testing.T) {
	withHandlerResilience(t, nil)
	body := `{"model":"claude-opus-4-6","system":"common system","messages":[{"role":"user","content":"first user"}],"max_tokens":1,"stream":false}`
	type result struct {
		client []byte
		bodies [][]byte
		calls  []l04HandlerStickyCall
	}
	var results []result
	for _, tc := range []struct {
		name    string
		caps    service.CacheM4BuildCapabilities
		enabled bool
	}{
		{"capability_false_database_enabled", service.CacheM4BuildCapabilities{}, true},
		{"master_off", service.CacheM4BuildCapabilities{L04: true}, false},
		{"on", service.CacheM4BuildCapabilities{L04: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, k, cache, up := l04HandlerFixture(t, tc.caps, tc.enabled, true)
			client := l04ServeHandler(t, h, k, "/v1/messages", body, (*GatewayHandler).Messages)
			require.Equal(t, []int64{1, 2}, up.ids, "actual prompt-too-long branch retries only in serving group B")
			require.Len(t, up.bodies, 2)
			byGroup := map[int64]string{}
			for _, c := range cache.calls {
				if c.hash != "" {
					if old, ok := byGroup[c.group]; ok {
						require.Equal(t, old, c.hash)
					}
					byGroup[c.group] = c.hash
				}
			}
			require.Contains(t, byGroup, int64(31))
			require.Contains(t, byGroup, int64(77))
			if tc.name == "on" {
				require.True(t, strings.HasPrefix(byGroup[31], "a2:"))
				require.True(t, strings.HasPrefix(byGroup[77], "a2:"))
				require.NotEqual(t, byGroup[31], byGroup[77])
			} else {
				require.Equal(t, byGroup[31], byGroup[77])
				require.False(t, strings.HasPrefix(byGroup[31], "a2:"))
			}
			results = append(results, result{client, l04PromptFallbackWireForComparison(t, up.bodies), cache.calls})
		})
	}
	require.Len(t, results, 3)
	require.Equal(t, results[0], results[1], "capability false with enabled DB retains baseline bytes and every sticky call")
	require.Equal(t, results[0].client, results[2].client)
	require.Equal(t, results[0].bodies, results[2].bodies, "a2 changes only local sticky identity")
}

func TestL04CompatActualHandlersOffOnOutboundBytes(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		path, body string
		call       func(*GatewayHandler, *gin.Context)
	}{
		{"/v1/chat/completions", `{"model":"claude-sonnet-4-5","messages":[{"role":"system","content":"system A"},{"role":"user","content":"hi"}],"stream":false}`, (*GatewayHandler).ChatCompletions},
		{"/v1/responses", `{"model":"claude-sonnet-4-5","instructions":"system B","input":"hi","stream":false}`, (*GatewayHandler).Responses},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var bodies [][][]byte
			var clients [][]byte
			var calls [][]l04HandlerStickyCall
			for _, enabled := range []bool{false, true} {
				h, k, cache, up := l04HandlerFixture(t, service.CacheM4BuildCapabilities{L04: true}, enabled, false)
				clients = append(clients, l04ServeHandler(t, h, k, tc.path, tc.body, tc.call))
				bodies = append(bodies, up.bodies)
				calls = append(calls, cache.calls)
				require.Equal(t, []int64{1}, up.ids)
				for _, call := range cache.calls {
					require.False(t, strings.HasPrefix(call.hash, "a2:"))
				}
			}
			require.Equal(t, bodies[0], bodies[1])
			require.Equal(t, clients[0], clients[1])
			require.Equal(t, calls[0], calls[1])
		})
	}
}
