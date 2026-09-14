package iqcheck

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryAfter(t *testing.T) {
	n := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	for _, v := range []string{"60", n.Add(time.Minute).Format(http.TimeFormat)} {
		at, p := RetryAfter(v, n)
		require.False(t, p)
		require.Equal(t, n.Add(time.Minute), *at)
	}
	at, p := RetryAfter(strings.Repeat("9", 100), n)
	require.Nil(t, at)
	require.True(t, p)
	at, p = RetryAfter("-1", n)
	require.Nil(t, at)
	require.False(t, p)
}
func TestIQMonitoringErrorCodes(t *testing.T) {
	for code, reason := range map[string]string{"insufficient_quota": "quota_exhausted", "invalid_api_key": "authentication_unavailable", "unsupported_parameter": "unsupported_parameter", "rate_limit_exceeded": "rate_limited"} {
		r := ParseHTTP(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"3600"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"secret"}}`))}, false, "http")
		require.Equal(t, reason, r.Reason)
		require.Equal(t, "unknown", r.Status)
		require.NotNil(t, r.Diagnostic.RetryAfter)
		require.NotContains(t, string(r.Diagnostic.JSON()), "secret")
	}
	r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"insufficient_quota\"}}}\n\n"))}, false, "plugin")
	require.Equal(t, "quota_exhausted", r.Reason)
	require.Equal(t, "plugin_attempts_unknown", r.Diagnostic.RetryVisibility)
}
