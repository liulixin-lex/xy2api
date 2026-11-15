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
