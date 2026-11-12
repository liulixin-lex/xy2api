package httpclient

import (
	"net/http"
	"strconv"
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
		require.Same(t, shared.Transport, client.Transport)
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
