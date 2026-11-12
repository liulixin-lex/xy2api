package websearch

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchProxyValidationIsFailClosedAndSanitized(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const username, password = "demo+user@example", "demo+secret:/@"
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			proxy := &url.URL{Scheme: scheme, Host: "proxy.example:8080", User: url.UserPassword(username, password)}
			client, err := newHTTPClient(proxy.String())
			require.NoError(t, err)
			transport, ok := client.Transport.(*http.Transport)
			require.True(t, ok)
			if scheme == "http" || scheme == "https" {
				configured, err := transport.Proxy(&http.Request{})
				require.NoError(t, err)
				require.Equal(t, proxy.User, configured.User)
			} else {
				require.NotNil(t, transport.DialContext)
			}
			for _, host := range []string{net.JoinHostPort("[", "8080"), ""} {
				proxy.Host = host
				raw := proxy.String()
				client, err := newHTTPClient(raw)
				require.Error(t, err)
				require.Nil(t, client, "invalid proxy must never produce a direct client")
				assertSearchProxySecretsAbsent(t, err.Error(), username, password)
				for _, accountProxy := range []bool{false, true} {
					logs.Reset()
					cfg := ProviderConfig{Type: "brave", APIKey: "synthetic-key", ProxyURL: raw}
					req := SearchRequest{Query: "test"}
					if accountProxy {
						cfg.ProxyURL = ""
						req.ProxyURL = raw
					}
					m := NewManager([]ProviderConfig{cfg}, nil)
					_, _, err = m.SearchWithBestProvider(context.Background(), req)
					require.Error(t, err)
					require.Empty(t, m.clientCache)
					require.Contains(t, logs.String(), `"level":"WARN"`)
					assertSearchProxySecretsAbsent(t, logs.String()+err.Error(), username, password)
				}
			}
		})
	}
}

func TestSearchClientCacheSeparatesProxyCredentials(t *testing.T) {
	m := NewManager(nil, nil)
	first, err := m.getOrCreateHTTPClient("http://demo-a:secret-a@proxy.example:8080")
	require.NoError(t, err)
	for _, raw := range []string{"http://demo-b:secret-a@proxy.example:8080", "http://demo-a:secret-b@proxy.example:8080"} {
		other, err := m.getOrCreateHTTPClient(raw)
		require.NoError(t, err)
		require.NotSame(t, first, other)
	}
	reused, err := m.getOrCreateHTTPClient("http://demo-a:secret-a@proxy.example:8080")
	require.NoError(t, err)
	require.Same(t, first, reused)
}

func assertSearchProxySecretsAbsent(t *testing.T, output string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		for _, variant := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), url.User(secret).String()} {
			require.NotContains(t, strings.ToLower(output), strings.ToLower(variant))
		}
	}
}
