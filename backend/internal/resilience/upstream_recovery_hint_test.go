//go:build unit

package resilience

import (
	"net/http"
	"testing"
	"time"
)

func TestFoundationUpstreamRecoveryHintState(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		h      http.Header
		body   string
		delay  time.Duration
		source string
	}{
		{http.Header{"Retry-After": {"3"}, "Retry-After-Ms": {"1000"}}, `{}`, 3 * time.Second, "http.headers"},
		{http.Header{"Retry-After": {"1"}}, `{"response":{"error":{"headers":{"retry-after-ms":4000}}},"error":{"headers":{"retry-after":"3"},"retry_after":2}}`, 4 * time.Second, "response.error.headers"},
		{nil, `{"error":{"headers":{"retry-after":2},"retry_after_ms":"5000","message":"try again in 1s"}}`, 5 * time.Second, "error.retry_after_ms"},
		{nil, `{"error":{"message":"try again in 1m30.5s then TRY AGAIN IN 2 minutes"}}`, 2 * time.Minute, "error.message"},
		{nil, `{"error":{"retry_after":0.0000000001}}`, time.Nanosecond, "error.retry_after"},
		{nil, `{"error":{"resets_in_seconds":3600}}`, time.Hour, "error.resets_in_seconds"},
		{http.Header{"Retry-After": {now.Add(time.Minute).Format(http.TimeFormat)}}, `{}`, time.Minute, "http.headers"},
		{http.Header{"Retry-After": {"bad"}, "Retry-After-Ms": {"-1"}}, `{"error":{"retry_after":0,"retry_after_ms":"NaN"}}`, 0, ""},
		{http.Header{"Retry-After": {"1\r\nx: 2"}}, `{"message":"try again in 60s"}`, 0, ""},
		{nil, `{"error":{"retry_after":"999999999999999999999"}}`, 0, ""},
		{nil, `{"error":{"retry_after":"0e10"}}`, 0, ""},
		{nil, `{"error":{"retry_after":"1/2"}}`, 0, ""},
		{nil, `{"error":{"retry_after":"0x1p2"}}`, 0, ""},
		{nil, `{"error":{"retry_after":"1e-1000"}}`, time.Nanosecond, "error.retry_after"},
	} {
		h, ok := ParseUpstreamRecoveryHint(tc.h, []byte(tc.body), now)
		if ok != (tc.delay > 0) || h.Delay != tc.delay || h.Source != tc.source {
			t.Fatalf("%+v: %+v %v", tc, h, ok)
		}
	}
	hint := UpstreamRecoveryHint{Headers: map[string]string{"retry-after": "3"}, NotBefore: now.Add(3 * time.Second)}
	s := &RequestState{}
	s.SetUpstreamRecoveryHint(&hint)
	s.NoteAccountRetryNotBefore(1, hint)
	hint.Headers["retry-after"] = "changed"
	got := s.UpstreamRecoveryHint()
	if got.Headers["retry-after"] != "3" {
		t.Fatal(got)
	}
	got.Headers["retry-after"] = "changed"
	if s.UpstreamRecoveryHint().Headers["retry-after"] != "3" {
		t.Fatal("aliased getter")
	}
	s.NoteAccountRetryNotBefore(1, UpstreamRecoveryHint{NotBefore: now.Add(time.Second)})
	s.SetUpstreamRecoveryHint(nil)
	kept, ok := s.AccountRetryNotBefore(1)
	if !ok || !kept.NotBefore.Equal(now.Add(3*time.Second)) {
		t.Fatal(kept, ok)
	}
	if _, ok := s.AccountRetryNotBefore(2); ok {
		t.Fatal("cross account")
	}
	if _, ok := (&RequestState{}).AccountRetryNotBefore(1); ok {
		t.Fatal("cross request")
	}
	target := &UpstreamAttemptTarget{AccountID: 1, Protocol: "responses", Origin: "https://example.test"}
	s.SetUpstreamAttemptTarget(target)
	target.AccountID = 2
	gotTarget := s.UpstreamAttemptTarget()
	gotTarget.AccountID = 3
	if s.UpstreamAttemptTarget().AccountID != 1 {
		t.Fatal("target alias")
	}
	s.SetUpstreamAttemptTarget(nil)
	if s.UpstreamAttemptTarget() != nil {
		t.Fatal("not cleared")
	}
	var absent *RequestState
	absent.SetUpstreamRecoveryHint(nil)
	absent.SetUpstreamAttemptTarget(nil)
	absent.NoteAccountRetryNotBefore(1, hint)
}

func TestFoundationRecoveryTrustedBody(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
		header     http.Header
		want       time.Duration
		source     string
	}{
		{"trusted_top_level", `{"type":"error","error":{"type":"rate_limit_error","message":"busy"},"retry_after":300}`, nil, 300 * time.Second, "retry_after"},
		{"latest_top_level", `{"type":"error","error":{"type":"rate_limit_error","retry_after":20},"retry_after_ms":300000}`, http.Header{"Retry-After": {"10"}}, 300 * time.Second, "retry_after_ms"},
		{"body_http_date", `{"error":{"message":"busy","retry_after":"Thu, 01 Jan 2026 00:01:00 GMT"}}`, nil, time.Minute, "error.retry_after"},
		{"top_http_date", `{"type":"error","error":{"message":"busy","retry_after":20},"retry_after":"Thu, 01 Jan 2026 00:01:00 GMT"}`, nil, time.Minute, "retry_after"},
		{"expired_date_keeps_valid_hint", `{"error":{"message":"busy","retry_after":"Wed, 31 Dec 2025 23:59:00 GMT","retry_after_ms":12000}}`, nil, 12 * time.Second, "error.retry_after_ms"},
		{"expired_date_only", `{"error":{"message":"busy","retry_after":"Wed, 31 Dec 2025 23:59:00 GMT"}}`, nil, 0, ""},
		{"ordinary_body", `{"type":"response.output_text.delta","delta":"Try again in 300s","retry_after":300,"retry_after_ms":300000}`, nil, 0, ""},
		{"ordinary_message", `{"message":"Try again in 300s","retry_after":300}`, nil, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseUpstreamRecoveryHint(tc.header, []byte(tc.body), now)
			if ok != (tc.want > 0) || got.Delay != tc.want || got.Source != tc.source {
				t.Fatalf("got %+v %v; want %v %q", got, ok, tc.want, tc.source)
			}
			if tc.source != "" && len(got.Headers) != 0 {
				t.Fatal("body source invented headers")
			}
		})
	}
}

func TestFoundationRecoveryCompoundZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		duration string
		want     time.Duration
	}{
		{"1m0.0s", time.Minute}, {"0.0m1s", time.Second}, {"00.00m0.0s", 0}, {"1m00.000s", time.Minute},
		{"1h9223372036854775807s", 0}, {"0.0m0.0000000001s", time.Nanosecond},
	} {
		t.Run(tc.duration, func(t *testing.T) {
			body := []byte(`{"error":{"message":"Try again in ` + tc.duration + `"}}`)
			got, ok := ParseUpstreamRecoveryHint(nil, body, now)
			if ok != (tc.want > 0) || got.Delay != tc.want {
				t.Fatalf("got %+v %v, want %v", got, ok, tc.want)
			}
		})
	}
}

func TestFoundationRecoveryFlatErrorMessage(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		body string
		want time.Duration
	}{
		{`{"type":"error","code":"rate_limit_exceeded","message":"... Please try again in 20s.","param":null}`, 20 * time.Second},
		{`{"type":"response.output_text.delta","message":"Please try again in 20s."}`, 0},
		{`{"message":"Please try again in 20s."}`, 0},
		{`{"type":"error","message":"Please try again in 60s.","error":{"message":"busy"}}`, 0},
		{`{"type":"error","message":"Please try again in 20s.","retry_after":30}`, 30 * time.Second},
	} {
		got, ok := ParseUpstreamRecoveryHint(nil, []byte(tc.body), now)
		if ok != (tc.want > 0) || got.Delay != tc.want {
			t.Fatalf("%s: %+v %v, want %s", tc.body, got, ok, tc.want)
		}
	}
}
