package resilience

import (
	"context"
	"sync"
	"testing"
)

func TestRetryAdviceContext(t *testing.T) {
	var nilContext context.Context
	if got := RetryAdviceFrom(nilContext); got != (RetryAdvice{}) {
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
	if SetRetryAdvice(root, a) || SetRetryAdvice(nilContext, a) {
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
