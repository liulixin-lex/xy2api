//go:build unit

package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestL01PreparationRequiresTenantScopedRoutingSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, source := range []string{"content", "explicit", "fallback"} {
		for _, tc := range []struct {
			name, platform string
			keyID          int64
			scoped         bool
		}{
			{"openai", PlatformOpenAI, 99, true},
			{"composite_tenant_a", PlatformComposite, 99, false},
			{"composite_tenant_b", PlatformComposite, 100, false},
			{"other_platform", PlatformAnthropic, 99, false},
			{"no_api_key", "", 0, false},
			{"grok", PlatformGrok, 99, false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-e9-same-content"}]}`)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(true, true))
				if tc.keyID != 0 {
					groupID := int64(10)
					c.Set("api_key", &APIKey{ID: tc.keyID, GroupID: &groupID, Group: &Group{ID: groupID, Platform: tc.platform}})
				}
				s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-e9-scope-secret"}}}
				var routing string
				switch source {
				case "content":
					routing = s.GenerateSessionHash(c, body)
				case "explicit":
					c.Request.Header.Set("session_id", "synthetic-e9-client-session")
					routing = s.GenerateExplicitSessionHash(c, body)
				case "fallback":
					routing = s.GenerateSessionHashWithFallback(c, []byte(`{}`), "synthetic-e9-fallback")
				}
				require.NotEmpty(t, routing)
				require.Equal(t, tc.scoped, strings.HasPrefix(routing, "v2:"))
				require.Equal(t, routing, c.GetString(openAICacheSessionGinKey))
				d := prepareOpenAIUpstreamIdentity(c, &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
				require.Equal(t, tc.scoped, d.eligible())
				if !tc.scoped {
					want := "no_routing_session"
					if tc.platform == PlatformGrok {
						want = "grok"
					}
					require.Equal(t, want, d.Skip)
					_, fixed := cachePolicyL01CohortFromGin(c)
					require.False(t, fixed, "legacy seed must never fix an L-01 cohort")
				}
			})
		}
	}
}

// This fixture attaches a trace to inspect the internal decision. Public composite
// ingress remains outside beginCacheTrace; see the platform-boundary test below.
func TestL01CompositeOpenAIForwardSkipsSharedRoutingIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const body = `{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-e9-same-content"}],"stream":false}`
	var sharedRouting string
	for _, keyID := range []int64{99, 100} {
		t.Run(fmt.Sprint(keyID), func(t *testing.T) {
			base := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(false, true), keyID, 10)
			candidate := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), keyID, 10)
			for _, f := range []l01ResponsesForwardFixture{base, candidate} {
				value, _ := f.context.Get("api_key")
				key, ok := value.(*APIKey)
				require.True(t, ok)
				key.Group = &Group{ID: 10, Platform: PlatformComposite}
			}
			baseResult := base.forward(t, body)
			result := candidate.forward(t, body)
			routing := candidate.context.GetString(openAICacheSessionGinKey)
			require.NotEmpty(t, routing)
			require.False(t, strings.HasPrefix(routing, "v2:"))
			require.Equal(t, base.context.GetString(openAICacheSessionGinKey), routing, "legacy sticky behavior is unchanged")
			if sharedRouting == "" {
				sharedRouting = routing
			} else {
				require.Equal(t, sharedRouting, routing, "regression really exercises the old cross-tenant shared seed")
			}
			require.True(t, candidate.account.IsOpenAIApiKey())
			require.False(t, candidate.trace.data.L01Applied)
			require.Equal(t, "no_routing_session", candidate.trace.data.L01Skip)
			require.Equal(t, "none", candidate.trace.data.L01Arm)
			_, fixed := cachePolicyL01CohortFromGin(candidate.context)
			require.False(t, fixed)
			require.False(t, gjson.GetBytes(candidate.upstream.lastBody, "prompt_cache_key").Exists())
			require.Empty(t, candidate.upstream.lastReq.Header.Get("session_id"))
			require.Equal(t, base.upstream.lastBody, candidate.upstream.lastBody)
			require.Equal(t, base.upstream.lastReq.Header, candidate.upstream.lastReq.Header)
			require.Equal(t, base.response.Body.String(), candidate.response.Body.String())
			require.Equal(t, baseResult.Usage, result.Usage)
		})
	}
}

func TestL01PolicyOffPrecedesRoutingPrefixGuard(t *testing.T) {
	for _, routing := range []string{"", "shared-legacy-seed", "v2:0123456789abcdef"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(false, true))
		c.Set(openAICacheSessionGinKey, routing)
		c.Set(openAICacheSessionProvenanceGinKey, true)
		c.Set(openAIUpstreamIdentityWireGinKey, "synthetic-prior-wire")
		d := prepareOpenAIUpstreamIdentity(c, &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
		require.Equal(t, "policy_off", d.Skip)
		require.False(t, d.eligible())
		require.Empty(t, d.RoutingSession)
		require.Equal(t, "synthetic-prior-wire", c.GetString(openAIUpstreamIdentityWireGinKey))
		_, fixed := cachePolicyL01CohortFromGin(c)
		require.False(t, fixed)
	}
}

func TestL01CompositeCacheTracePlatformBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{PlatformComposite, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-e9-trace-boundary"}]}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			groupID := int64(10)
			c.Set("api_key", &APIKey{ID: 99, GroupID: &groupID, Group: &Group{ID: groupID, Platform: platform}})
			s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-e9-scope-secret"}}}
			s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			l01WireConfigure(t, l01DisabledWireState{master: true, observer: true, mode: "uuid_fill", percent: 100})(s)
			require.True(t, s.CaptureCachePolicy(c).TraceEnabled(), "exercise the platform gate with observation enabled")
			require.NotEmpty(t, s.GenerateSessionHash(c, body))
			s.beginCacheTrace(c, body)
			d := prepareOpenAIUpstreamIdentity(c, &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
			if platform == PlatformComposite {
				require.Nil(t, cacheTraceFromGin(c), "legacy composite ingress remains outside trace platform scope")
				require.Equal(t, "no_routing_session", d.Skip)
				require.False(t, d.eligible())
				_, fixed := cachePolicyL01CohortFromGin(c)
				require.False(t, fixed)
			} else {
				require.NotNil(t, cacheTraceFromGin(c))
				require.True(t, d.eligible(), "normal tenant-scoped OpenAI requests remain eligible")
			}
		})
	}
}
