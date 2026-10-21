package resilience

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryAdviceProviderResolvesCurrentSnapshot(t *testing.T) {
	var seconds atomic.Int64
	var calls atomic.Int64
	var hasState atomic.Bool
	seconds.Store(1)
	ctx := WithRetryAdviceProvider(context.Background(), func(ctx context.Context) RetryAdvice {
		calls.Add(1)
		hasState.Store(StateFrom(ctx) != nil)
		return RetryAdvice{Seconds: int(seconds.Load()), Source: "recovery"}
	})
	require.Zero(t, calls.Load(), "installation must not query candidates")
	type childKey struct{}
	child := context.WithValue(ctx, childKey{}, true)
	require.Equal(t, 1, ResolveRetryAdvice(child).Seconds)
	require.True(t, hasState.Load())
	seconds.Store(7)
	require.Equal(t, 7, ResolveRetryAdvice(child).Seconds)
	require.Equal(t, int64(2), calls.Load())

	// An explicit unknown is also final handler advice, not a cache miss.
	require.True(t, SetRetryAdvice(ctx, RetryAdvice{}))
	require.Equal(t, RetryAdvice{}, ResolveRetryAdvice(child))
	require.Equal(t, int64(2), calls.Load())
	require.True(t, SetRetryAdvice(ctx, RetryAdvice{Suppress: true, Source: "turn_cap"}))
	require.True(t, ResolveRetryAdvice(child).Suppress)
}

func TestRetryAdviceMessageMatchesHeaders(t *testing.T) {
	for _, tc := range []struct {
		advice RetryAdvice
		want   string
	}{
		{RetryAdvice{Seconds: 7}, "Busy. Please try again in 7s."},
		{RetryAdvice{Seconds: 60}, "Busy. Please try again in 10s."},
		{RetryAdvice{Suppress: true, Seconds: 7}, "Busy."},
		{RetryAdvice{}, "Busy."},
	} {
		require.Equal(t, tc.want, tc.advice.Message("Busy. Please try again in 30s."))
	}
	require.Equal(t, "Consult the administrator.", (RetryAdvice{}).Message("Consult the administrator."))
}

func TestRetryAdviceProviderBoundedAndDirectAdviceWins(t *testing.T) {
	var nilContext context.Context
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	ctx := WithRetryAdviceProvider(nilContext, func(context.Context) RetryAdvice {
		close(started)
		<-release // A broken provider must not hold up a terminal frame.
		return RetryAdvice{Seconds: 9}
	})
	done := make(chan RetryAdvice, 1)
	go func() { done <- ResolveRetryAdvice(ctx) }()
	<-started
	SetRetryAdvice(ctx, RetryAdvice{Suppress: true, Source: "content_rejection"})
	select {
	case advice := <-done:
		require.True(t, advice.Suppress)
	case <-time.After(time.Second):
		t.Fatal("retry advice exceeded its 100 ms provider budget")
	}
}

func TestRetryAdviceProviderTimeoutAndCancellation(t *testing.T) {
	var nilContext context.Context
	for _, canceled := range []bool{false, true} {
		parent, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		ctx := WithRetryAdviceProvider(parent, func(ctx context.Context) RetryAdvice {
			<-ctx.Done()
			return RetryAdvice{Seconds: 9}
		})
		require.Equal(t, RetryAdvice{}, ResolveRetryAdvice(ctx), "expired results must be discarded")
		cancel()
	}
	require.Equal(t, RetryAdvice{}, ResolveRetryAdvice(nilContext))
	require.Equal(t, RetryAdvice{}, ResolveRetryAdvice(context.Background()))
	require.Equal(t, RetryAdvice{}, ResolveRetryAdvice(WithRetryAdviceProvider(nilContext, nil)))
}

func TestRetryAdviceMessagePreservesSentenceAndNonemptyFallback(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Busy. Please try again in 30s.", "Busy."},
		{"Busy! Please try again in 30s.", "Busy!"},
		{"Please try again in 30s.", "Please try again in 30s."},
		{"Busy. Please try again in 30s. More detail.", "Busy. Please try again in 30s. More detail."},
		{"Busy.Please try again in 30s.", "Busy.Please try again in 30s."},
		{"Busy. Please try again in 30s", "Busy. Please try again in 30s"},
	} {
		require.Equal(t, tc.want, (RetryAdvice{Suppress: true}).Message(tc.input))
	}
	require.Equal(t, "Please try again in 7s.", (RetryAdvice{Seconds: 7}).Message("Please try again in 30s."))
}

func TestRetryAdviceMessagePreservesOriginalPunctuation(t *testing.T) {
	for _, original := range []string{"Busy!", "Unavailable?", "Upstream failed.", "上游暂不可用。"} {
		t.Run(original, func(t *testing.T) {
			withHint := (RetryAdvice{Seconds: 7}).Message(original)
			require.Equal(t, original+" Please try again in 7s.", withHint)
			require.Equal(t, original, (RetryAdvice{Suppress: true}).Message(withHint))
			require.Equal(t, original, (RetryAdvice{}).Message(withHint))
		})
	}
}
