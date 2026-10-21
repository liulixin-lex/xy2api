package resilience

import (
	"context"
	"sync"
	"testing"
)

func TestRetryAdviceContext(t *testing.T) {
	if got := RetryAdviceFrom(nil); got != (RetryAdvice{}) {
		t.Fatalf("nil context advice = %#v", got)
	}
	root := context.Background()
	a := RetryAdvice{Seconds: 7, Source: "recovery"}
	ctx := WithRetryAdvice(root, a)
	if got := RetryAdviceFrom(ctx); got != a {
		t.Fatalf("stored advice = %#v, want %#v", got, a)
	}
	if got := RetryAdviceFrom(root); got != (RetryAdvice{}) {
		t.Fatalf("advice leaked into parent: %#v", got)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if !SetRetryAdvice(ctx, RetryAdvice{Suppress: true, Source: "turn_cap"}) {
		t.Fatal("request state did not accept final advice")
	}
	if got := RetryAdviceFrom(child); !got.Suppress || got.Seconds != 0 {
		t.Fatalf("already captured child context has stale advice: %#v", got)
	}
	SetRetryAdvice(ctx, a)
	if got := RetryAdviceFrom(child); !got.Suppress || got.Seconds != 0 {
		t.Fatalf("a later hint revived terminal advice: %#v", got)
	}
	if SetRetryAdvice(root, a) || SetRetryAdvice(nil, a) {
		t.Fatal("advice was stored without request state")
	}
}

func TestRetryAdviceConcurrentAndRequestIsolation(t *testing.T) {
	ctx := WithRetryAdvice(context.Background(), RetryAdvice{})
	other := WithRetryAdvice(context.Background(), RetryAdvice{Seconds: 1, Source: "healthy_candidate"})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			for j := 0; j < 50; j++ {
				SetRetryAdvice(ctx, RetryAdvice{Seconds: 5, Source: "recovery"})
				_ = RetryAdviceFrom(ctx)
			}
		})
	}
	wg.Wait()
	if got := RetryAdviceFrom(other); got.Seconds != 1 || got.Source != "healthy_candidate" {
		t.Fatalf("advice leaked across requests: %#v", got)
	}
}

func TestRetryAdviceHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		advice RetryAdvice
		want   string
	}{
		{"unknown", RetryAdvice{}, ""},
		{"healthy", RetryAdvice{Seconds: 1}, "1"},
		{"recovery", RetryAdvice{Seconds: 7}, "7"},
		{"ceiling", RetryAdvice{Seconds: 30}, "10"},
		{"negative", RetryAdvice{Seconds: -1}, ""},
		{"terminal", RetryAdvice{Seconds: 7, Suppress: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.advice.Headers()
			if h["retry-after"] != tc.want {
				t.Fatalf("headers = %#v, want retry-after %q", h, tc.want)
			}
			if tc.want == "" && h != nil {
				t.Fatalf("absent advice must omit headers: %#v", h)
			}
			if len(h) > 1 {
				t.Fatalf("unexpected header propagation: %#v", h)
			}
		})
	}
}

func TestRetryAdviceEligible(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		typ, code string
		want      bool
	}{
		{"rate limit", 429, "rate_limit_error", "", true},
		{"upstream failure", 502, "api_error", "", true},
		{"capacity", 0, "overloaded_error", "server_is_overloaded", true},
		{"synthetic upstream", 0, "api_error", "upstream_error", true},
		{"missing model", 503, "api_error", "model_not_found", false},
		{"not found", 0, "not_found_error", "", false},
		{"billing service", 503, "billing_service_error", "", false},
		{"invalid request", 0, "invalid_request_error", "", false},
		{"context length", 0, "api_error", "context_length_exceeded", false},
		{"balance", 429, "insufficient_quota", "", false},
		{"content", 503, "api_error", "content_policy_violation", false},
		{"other client error", 422, "api_error", "", false},
		{"unknown", 0, "unknown_error", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetryAdviceEligible(tc.status, tc.typ, tc.code); got != tc.want {
				t.Fatalf("RetryAdviceEligible(%d, %q, %q) = %v, want %v", tc.status, tc.typ, tc.code, got, tc.want)
			}
		})
	}
}
