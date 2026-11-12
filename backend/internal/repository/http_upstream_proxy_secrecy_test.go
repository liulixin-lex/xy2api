package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyLogAddressOnlyIncludesPublicEndpoint(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", "direct"},
		{"http://demo:secret@proxy.example:8080/private?token=secret#secret", "http://proxy.example:8080"},
		{"https://demo:secret@[::1]:8443/", "https://[::1]:8443"},
		{"socks5://demo:secret@proxy.example:1080", "socks5h://proxy.example:1080"},
		{"http://demo:secret@proxy.example/private", "http://proxy.example:80"},
		{"https://demo:secret@[::1]/", "https://[::1]:443"},
		{"socks5h://demo:secret@proxy.example", "socks5h://proxy.example:1080"},
		{"http://demo:secret@[:8080", "invalid"},
	} {
		require.Equal(t, tc.want, proxyLogAddress(tc.raw))
	}
}

func TestProxyPoolLogFieldsDoNotDependOnCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	svc, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	for _, credentials := range [][2]string{{"demo-a", "secret-a"}, {"demo-b", "secret-b"}} {
		proxy := (&url.URL{Scheme: "http", Host: "proxy.example:8080", User: url.UserPassword(credentials[0], credentials[1]), Path: "/private-route", RawQuery: "token=" + credentials[1], Fragment: credentials[1]}).String()
		_, err := svc.getClientEntryWithTLS(proxy, 7, 1, &tlsfingerprint.Profile{Name: "test"}, service.HTTPUpstreamProfileOpenAI, false, false)
		require.NoError(t, err)
	}
	var created []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))
		if event["msg"] == "tls_fingerprint_creating_new_client" {
			delete(event, "time")
			created = append(created, event)
		}
	}
	require.Len(t, created, 2)
	require.Equal(t, created[0], created[1], "log fields must not encode credential differences")
	require.Equal(t, "http://proxy.example:8080", created[0]["proxy"])
	require.Equal(t, "openai", created[0]["profile"])
	require.Equal(t, float64(7), created[0]["account_id"])
	require.NotContains(t, logs.String(), "sha256:")
	require.NotContains(t, logs.String(), `"cache_key"`)
	require.NotContains(t, logs.String(), `"pool_fingerprint"`)
	require.NotContains(t, logs.String(), `"proxy_fingerprint"`)
}

func TestHTTPUpstreamProxyCredentialsStayOutOfLogsAndErrors(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const username, password = "demo+user@example", "demo+secret:/@"
	profile := &tlsfingerprint.Profile{Name: "test"}
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			logs.Reset()
			cfg := &config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationProxy, MaxIdleConns: 16, OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
				Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 1,
			}}}
			svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
			require.True(t, ok)
			proxy := &url.URL{Scheme: scheme, Host: "proxy.example:8080", User: url.UserPassword(username, password)}
			raw := proxy.String()
			entry, err := svc.getClientEntryWithTLS(raw, 7, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			reused, err := svc.getClientEntryWithTLS(raw, 7, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			require.Same(t, entry, reused)
			cfg.Gateway.MaxIdleConns++
			_, err = svc.getClientEntryWithTLS(raw, 7, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			require.Contains(t, logs.String(), "tls_fingerprint_evicting_stale_client")
			require.Contains(t, logs.String(), "tls_fingerprint_reusing_client")

			// A local host rejected by the request policy exercises the entry log without making a connection.
			req, err := http.NewRequestWithContext(service.WithHTTPUpstreamPublicHostsOnly(context.Background()), http.MethodGet, "https://127.0.0.1/", nil)
			require.NoError(t, err)
			_, err = svc.DoWithTLS(req, raw, 7, 1, profile)
			require.Error(t, err)
			assertProxySecretsAbsent(t, logs.String()+err.Error(), username, password)

			key, _, err := normalizeProxyURL(raw)
			require.NoError(t, err)
			svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, key, errors.New("http2: protocol error"))
			if scheme == "http" || scheme == "https" {
				require.True(t, svc.isOpenAIHTTP2FallbackActive(key))
				fallback, err := svc.getClientEntry(raw, 7, 1, service.HTTPUpstreamProfileOpenAI, false, false)
				require.NoError(t, err)
				require.Equal(t, upstreamProtocolModeOpenAIH1Fallback, fallback.protocolMode)
				transport, ok := fallback.client.Transport.(*http.Transport)
				require.True(t, ok)
				require.False(t, transport.ForceAttemptHTTP2)
				require.Contains(t, logs.String(), "openai_http2_proxy_fallback_activated")
			}
			proxy.Host = net.JoinHostPort("[", "8080")
			_, _, err = normalizeProxyURL(proxy.String())
			require.Error(t, err)
			_, err = svc.DoWithTLS(req, proxy.String(), 7, 1, profile)
			require.Error(t, err)
			assertProxySecretsAbsent(t, logs.String()+err.Error(), username, password)
			require.Contains(t, logs.String(), `"proxy":"invalid"`)
			require.NotContains(t, logs.String(), "sha256:")
			require.NotContains(t, logs.String(), `"cache_key"`)
			require.NotContains(t, logs.String(), `"pool_fingerprint"`)
			require.NotContains(t, logs.String(), `"proxy_fingerprint"`)
		})
	}
}

func TestHTTPUpstreamPoolsSeparateProxyCredentials(t *testing.T) {
	for _, isolation := range []string{config.ConnectionPoolIsolationProxy, config.ConnectionPoolIsolationAccountProxy} {
		t.Run(isolation, func(t *testing.T) {
			svc, ok := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: isolation}}).(*httpUpstreamService)
			require.True(t, ok)
			for _, tlsEnabled := range []bool{false, true} {
				var entries []*upstreamClientEntry
				for _, credentials := range [][2]string{{"demo-a", "secret-a"}, {"demo-b", "secret-a"}, {"demo-a", "secret-b"}} {
					proxy := (&url.URL{Scheme: "http", Host: "proxy.example:8080", User: url.UserPassword(credentials[0], credentials[1])}).String()
					get := func() (*upstreamClientEntry, error) {
						if tlsEnabled {
							return svc.getClientEntryWithTLS(proxy, 7, 1, &tlsfingerprint.Profile{Name: "test"}, service.HTTPUpstreamProfileDefault, false, false)
						}
						return svc.getOrCreateClient(proxy, 7, 1)
					}
					entry, err := get()
					require.NoError(t, err)
					reused, err := get()
					require.NoError(t, err)
					require.Same(t, entry, reused)
					for _, other := range entries {
						require.NotSame(t, entry, other)
						require.NotEqual(t, entry.proxyKey, other.proxyKey)
					}
					entries = append(entries, entry)
				}
			}
		})
	}
}

func assertProxySecretsAbsent(t *testing.T, output string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		for _, variant := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), url.User(secret).String()} {
			require.NotContains(t, strings.ToLower(output), strings.ToLower(variant))
		}
	}
}
