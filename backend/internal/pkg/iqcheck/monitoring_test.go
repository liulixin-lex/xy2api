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
		r := ParseHTTP(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"3600"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"secret"}}`))}, false, "http", fixtureCheck)
		require.Equal(t, reason, r.Reason)
		require.Equal(t, "unknown", r.Status)
		require.NotNil(t, r.Diagnostic.RetryAfter)
		require.NotContains(t, string(r.Diagnostic.JSON()), "secret")
	}
	r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"insufficient_quota\"}}}\n\n"))}, false, "plugin", fixtureCheck)
	require.Equal(t, "quota_exhausted", r.Reason)
	require.Equal(t, "plugin_attempts_unknown", r.Diagnostic.RetryVisibility)
}

func TestRetryableFollowsFailurePolicyForServerErrorsAndParseFailures(t *testing.T) {
	for _, result := range []Result{
		{Status: "unknown", Reason: "http_500"},
		{Status: "unknown", Reason: "unparseable_answer", Diagnostic: &Diagnostic{Stage: "parse"}},
	} {
		require.True(t, Retryable(result), result.Reason)
	}
	require.False(t, Retryable(Unknown("authentication_unavailable")))
}

func TestIQRoundStopsOnResourceFailuresWithoutChangingValidAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter, body, want string
		httpStatus                   int
	}{
		{name: "forbidden", httpStatus: 403, body: `{}`, want: "unknown"},
		{name: "limited", httpStatus: 429, body: `{}`, want: "unknown"},
		{name: "rewritten forbidden", httpStatus: 403, body: `{"error":{"code":"server_error"}}`, want: "unknown"},
		{name: "rewritten limited", httpStatus: 429, body: `{"error":{"code":"server_error"}}`, want: "unknown"},
		{name: "body rate limit", httpStatus: 503, body: `{"error":{"code":"rate_limit_exceeded"}}`, want: "unknown"},
		{name: "retry floor", httpStatus: 503, retryAfter: "60", body: `{}`, want: "unknown"},
		{name: "unbounded retry", httpStatus: 503, retryAfter: strings.Repeat("9", 100), body: `{}`, want: "unknown"},
		{name: "correct with retry floor", httpStatus: 200, retryAfter: "60", body: `{"choices":[{"message":{"content":"21"},"finish_reason":"stop"}]}`, want: "smart"},
		{name: "wrong with retry floor", httpStatus: 200, retryAfter: "60", body: `{"choices":[{"message":{"content":"29"},"finish_reason":"stop"}]}`, want: "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseHTTP(&http.Response{StatusCode: tc.httpStatus, Header: http.Header{"Retry-After": []string{tc.retryAfter}}, Body: io.NopCloser(strings.NewReader(tc.body))}, true, "http", fixtureCheck)
			require.Equal(t, tc.want, r.Status, r.Reason)
			require.False(t, Retryable(r))
			mode := "normal"
			if r.Status == "degraded" {
				mode = "recheck"
			}
			status, done := DecideRound(mode, []Outcome{{Status: r.Status, Retryable: Retryable(r)}})
			require.True(t, done)
			require.Equal(t, tc.want, status)
			if tc.retryAfter == "60" {
				schedule := NextSchedule(time.Now(), time.Minute, 0, 0, r, 0)
				require.Equal(t, r.Diagnostic.RetryAfter, schedule.NotBefore)
			}
		})
	}
}
