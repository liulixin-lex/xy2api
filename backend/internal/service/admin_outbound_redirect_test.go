package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

var adminOutboundStatuses = []int{200, 301, 302, 303, 307, 308}

// The server also acts as a forward proxy: a followed redirect reaches /second
// in either mode, including redirects that would replay passwords/assertions.
func adminOutboundServer(t *testing.T, status int, successBody string, check func(*http.Request)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	second := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			second.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, successBody)
			return
		}
		check(r)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, successBody)
			return
		}
		w.Header().Set("Location", "http://redirect-user:redirect-password@"+r.Host+"/second?token=query-secret#fragment-secret")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "body-secret")
	}))
	t.Cleanup(server.Close)
	return server, second
}

func requireAdminRedirectError(t *testing.T, status int, err error) {
	t.Helper()
	require.EqualError(t, err, fmt.Sprintf("redirect refused: status=%d host=127.0.0.1", status))
}

func TestContentModerationRejectsRedirects(t *testing.T) {
	for _, engine := range []string{ContentModerationEngineOpenAI, ContentModerationEngineTypeSafe} {
		for _, mode := range []string{"direct", "default", "proxy"} {
			for _, status := range adminOutboundStatuses {
				t.Run(fmt.Sprintf("%s/%s/%d", engine, mode, status), func(t *testing.T) {
					success := `{"results":[{"flagged":false}]}`
					if engine == ContentModerationEngineTypeSafe {
						answers := make(map[string]any)
						for category := range typeSafeModerationRules {
							answers[category] = map[string]any{"type": "noul", "noul": 0}
						}
						raw, err := json.Marshal(map[string]any{"model": "test-model", "answers": answers})
						require.NoError(t, err)
						success = string(raw)
					}
					server, second := adminOutboundServer(t, status, success, func(r *http.Request) {
						require.Equal(t, "Bearer api-secret", r.Header.Get("Authorization"))
						body, err := io.ReadAll(r.Body)
						require.NoError(t, err)
						require.Contains(t, string(body), "private-prompt")
					})
					svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil, nil)
					cfg := defaultContentModerationConfig()
					cfg.Engine, cfg.BaseURL = engine, server.URL
					if mode == "default" {
						svc.httpClient = nil
					}
					if mode == "proxy" {
						parsed, err := url.Parse(server.URL)
						require.NoError(t, err)
						port, err := strconv.Atoi(parsed.Port())
						require.NoError(t, err)
						svc.proxyRepo = &contentModerationTestProxyRepo{proxies: map[int64]*Proxy{
							7: {ID: 7, Protocol: "http", Host: parsed.Hostname(), Port: port, Status: StatusActive},
						}}
						cfg.ProxyID = moderationProxyIDPtr(7)
					}
					var gotStatus int
					result, err := svc.callModerationOnceWithInput(context.Background(), cfg, "api-secret", "private-prompt", &gotStatus)
					require.Zero(t, second.Load(), "redirect destination must never receive the request")
					require.Equal(t, status, gotStatus)
					if status == http.StatusOK {
						require.NoError(t, err)
						require.NotNil(t, result)
					} else {
						requireAdminRedirectError(t, status, err)
					}
				})
			}
		}
	}
}

func TestCRSRejectsRedirects(t *testing.T) {
	for _, stage := range []string{"login", "export"} {
		for _, status := range adminOutboundStatuses {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				var second atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/second" {
						second.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						return
					}
					login := r.URL.Path == "/web/auth/login"
					if login {
						var payload map[string]string
						require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
						require.Equal(t, "login-password", payload["password"])
					} else {
						require.Equal(t, "Bearer export-secret", r.Header.Get("Authorization"))
					}
					if status != http.StatusOK && ((stage == "login") == login) {
						w.Header().Set("Location", "http://redirect-user:redirect-password@"+r.Host+"/second?token=query-secret")
						w.WriteHeader(status)
						_, _ = io.WriteString(w, "body-secret login-password export-secret")
						return
					}
					if login {
						_, _ = io.WriteString(w, `{"success":true,"token":"export-secret"}`)
					} else {
						_, _ = io.WriteString(w, `{"success":true}`)
					}
				}))
				defer server.Close()
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				svc := &CRSSyncService{cfg: cfg}
				result, err := svc.fetchCRSExport(context.Background(), server.URL, "login-user", "login-password")
				require.Zero(t, second.Load(), "CRS password/token must not reach the redirect target")
				if status == http.StatusOK {
					require.NoError(t, err)
					require.True(t, result.Success)
				} else {
					requireAdminRedirectError(t, status, err)
				}
			})
		}
	}
}

func TestVertexRejectsRedirects(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	for _, mode := range []string{"direct", "proxy"} {
		for _, status := range adminOutboundStatuses {
			t.Run(fmt.Sprintf("%s/%d", mode, status), func(t *testing.T) {
				server, second := adminOutboundServer(t, status, `{"access_token":"access-secret","expires_in":3600}`, func(r *http.Request) {
					require.NoError(t, r.ParseForm())
					require.NotEmpty(t, r.PostForm.Get("assertion"))
				})
				proxyURL := ""
				if mode == "proxy" {
					proxyURL = server.URL
				}
				key := &vertexServiceAccountKey{ClientEmail: "service@example.test", PrivateKey: string(privatePEM), TokenURI: server.URL + "/token"}
				token, ttl, err := exchangeVertexServiceAccountToken(context.Background(), key, proxyURL)
				require.Zero(t, second.Load(), "Vertex assertion must not reach the redirect target")
				if status == http.StatusOK {
					require.NoError(t, err)
					require.Equal(t, "access-secret", token)
					require.Positive(t, ttl)
				} else {
					requireAdminRedirectError(t, status, err)
				}
			})
		}
	}
}
