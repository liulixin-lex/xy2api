package httpclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWithoutRedirectsPreservesSharedClients(t *testing.T) {
	for _, proxy := range []string{"", "http://proxy.example.test:8080"} {
		shared, err := GetClient(Options{ProxyURL: proxy, Timeout: 17 * time.Second})
		require.NoError(t, err)
		client := WithoutRedirects(shared)
		require.NotSame(t, shared, client)
		require.Equal(t, shared.Timeout, client.Timeout)
		sanitizer, ok := client.Transport.(*locationSanitizer)
		require.True(t, ok)
		require.Same(t, shared.Transport, sanitizer.base)
		require.Equal(t, shared.Jar, client.Jar)
		require.Nil(t, shared.CheckRedirect)
		require.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
		cached, err := GetClient(Options{ProxyURL: proxy, Timeout: 17 * time.Second})
		require.NoError(t, err)
		require.Same(t, shared, cached)
		require.Nil(t, cached.CheckRedirect)
	}
	client := WithoutRedirects(nil)
	require.NotSame(t, http.DefaultClient, client)
	require.Nil(t, http.DefaultClient.CheckRedirect)
	require.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

func TestRedirectErrorRevealsOnlyStatusAndHost(t *testing.T) {
	for _, tc := range []struct{ location, host string }{
		{"https://private-user:private-password@target.example.test:443/private-path?token=query-secret#fragment-secret", "target.example.test"},
		{"//private-user:private-password@target.example.test/private-path?token=query-secret", "target.example.test"},
		{"/private-path?token=query-secret#fragment-secret", "initial.example.test"},
		{"http://[::1]:8080/private-path?token=query-secret", "::1"},
		{"", "unknown"},
		{"http://invalid%zz/private-path?token=query-secret", "unknown"},
	} {
		for _, status := range []int{300, 301, 302, 303, 304, 307, 308, 399} {
			req, err := http.NewRequest(http.MethodPost, "https://initial.example.test/path", nil)
			require.NoError(t, err)
			resp := &http.Response{StatusCode: status, Request: req, Header: http.Header{"Location": {tc.location}}}
			err = RedirectError(resp)
			require.Error(t, err)
			require.Equal(t, "redirect refused: status="+strconv.Itoa(status)+" host="+tc.host, err.Error())
		}
	}
	for _, status := range []int{200, 299, 400, 500} {
		require.NoError(t, RedirectError(&http.Response{StatusCode: status}))
	}
}

func TestWithoutRedirectsClosesUnderlyingIdleConnections(t *testing.T) {
	base := &idleClosingTransport{}
	WithoutRedirects(&http.Client{Transport: base}).CloseIdleConnections()
	require.True(t, base.closed)
}

type idleClosingTransport struct {
	closed bool
}

func (*idleClosingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

func (t *idleClosingTransport) CloseIdleConnections() { t.closed = true }

func TestWithoutRedirectsSanitizesRealRedirectResponsesBeforeClientParsing(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var second atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/second" {
					second.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					return
				}
				w.Header().Set("Location", "http://redirect-user:redirect-password@"+r.Host+"/%zz?token=query-secret#fragment-secret")
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := WithoutRedirects(server.Client())
			resp, err := client.Get(server.URL + "/start")
			require.NoError(t, err)
			require.Zero(t, second.Load())
			require.Equal(t, status, resp.StatusCode)
			require.Empty(t, resp.Header.Values("Location"), "malformed Location is removed before Client.Do parses it")
			redirectErr := RedirectError(resp)
			require.EqualError(t, redirectErr, "redirect refused: status="+strconv.Itoa(status)+" host=unknown")
			require.NoError(t, resp.Body.Close())
		})
	}

	t.Run("parseable location retains only scheme and host", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://redirect-user:redirect-password@target.example.test:9443/private-path?token=query-secret#fragment-secret")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}))
		defer server.Close()

		resp, err := WithoutRedirects(server.Client()).Get(server.URL)
		require.NoError(t, err)
		require.Equal(t, []string{"https://target.example.test:9443"}, resp.Header.Values("Location"))
		redirectErr := RedirectError(resp)
		require.EqualError(t, redirectErr, "redirect refused: status=307 host=target.example.test")
		require.NotContains(t, redirectErr.Error(), "redirect-password")
		require.NotContains(t, redirectErr.Error(), "query-secret")
		require.NotContains(t, redirectErr.Error(), "fragment-secret")
		require.NoError(t, resp.Body.Close())
	})

	t.Run("missing location returns original redirect response", func(t *testing.T) {
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusPermanentRedirect, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
		})
		client := WithoutRedirects(&http.Client{Transport: transport})
		resp, err := client.Get("https://initial.example.test/start")
		require.NoError(t, err)
		require.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
		require.Empty(t, resp.Header.Values("Location"))
		require.EqualError(t, RedirectError(resp), "redirect refused: status=308 host=unknown")
		require.NoError(t, resp.Body.Close())
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
