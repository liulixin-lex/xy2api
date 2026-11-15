//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestM7AnthropicEndpointURLs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	svc := &GatewayService{cfg: cfg}
	native := &OpenAIGatewayService{cfg: cfg}
	body := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`)
	builders := []struct {
		name, endpoint string
		beta           bool
		build          func(*gin.Context, *Account) (*url.URL, error)
	}{
		{"messages", "/messages", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, _, err := svc.buildUpstreamRequest(context.Background(), c, a, body, "fixture", "apikey", "claude-sonnet-5-5", false, false)
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"passthrough", "/messages", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, a, body, "fixture")
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"count", "/messages/count_tokens", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, _, err := svc.buildCountTokensRequest(context.Background(), c, a, body, "fixture", "apikey", "claude-sonnet-5-5", false)
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"count_passthrough", "/messages/count_tokens", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(context.Background(), c, a, body, "fixture")
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"native", "/messages", false, func(_ *gin.Context, a *Account) (*url.URL, error) {
			a.Platform = PlatformZhipu
			raw, err := native.nativeAnthropicTargetURL(a)
			if err != nil {
				return nil, err
			}
			return url.Parse(raw)
		}},
		{"models", "/models", false, func(_ *gin.Context, a *Account) (*url.URL, error) {
			return url.Parse(buildV1ModelsURL(a.GetBaseURL()))
		}},
	}
	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			for _, base := range []struct{ suffix, prefix string }{
				{"", "/v1"}, {"/", "/v1"}, {"/v1", "/v1"}, {"/v1/", "/v1"},
				{"/api/v2beta", "/api/v2beta"}, {"/v1" + b.endpoint, "/v1"}, {"/v1" + b.endpoint + "/", "/v1"},
			} {
				for _, query := range []string{"", "?region=fixture&beta=false&beta=false#fragment"} {
					t.Run(base.suffix+query, func(t *testing.T) {
						a := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com" + base.suffix + query, "api_protocol": "anthropic"}}
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
						u, err := b.build(c, a)
						require.NoError(t, err)
						require.Equal(t, base.prefix+b.endpoint, u.Path)
						require.Empty(t, u.Fragment)
						if query != "" {
							require.Equal(t, "fixture", u.Query().Get("region"))
						}
						if b.beta {
							require.Equal(t, []string{"true"}, u.Query()["beta"])
						} else if query == "" {
							require.Empty(t, u.RawQuery)
						}
					})
				}
			}
			if b.name == "models" {
				return
			}
			cfg.Security.URLAllowlist.Enabled = true
			cfg.Security.URLAllowlist.UpstreamHosts = []string{"allowed.example"}
			defer func() { cfg.Security.URLAllowlist.Enabled = false }()
			a := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://blocked.invalid/v1", "api_protocol": "anthropic"}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			_, err := b.build(c, a)
			require.Error(t, err)
		})
	}
}
