package service

import (
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCacheProviderHistoryTenantAndEpochIsolation(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	first := s.cacheProviderSessionScope(PlatformAnthropic, 11, 101, "same-content")
	second := s.cacheProviderSessionScope(PlatformAnthropic, 12, 101, "same-content")
	caps := CachePolicyCaps{MaxSessions: 2, ChainsPerSession: 3, MaxCheckpoints: 512}
	var history cacheProviderTraceHistory
	now := time.Now()
	a := history.send(first, 1, now, caps)
	chain := cachePrefixChain{hashes: []uint64{123}, blocks: 1, anchor: "synthetic-anchor"}
	_, owner := history.settle(first, a, now.Add(time.Second))
	require.True(t, history.remember(first, owner, chain, 2000, now.Add(time.Second), caps))
	b := history.send(second, 2, now.Add(2*time.Second), caps)
	require.Empty(t, history.snapshot(second, b.owner, b.sentAt)[0].hashes,
		"same group and content from another API key must remain cold")
	require.Equal(t, 2000, history.snapshot(first, a.owner, b.sentAt)[0].prompt)
	require.Empty(t, history.snapshot(first, a.owner, now)[0].hashes,
		"a request cannot compare with a future settlement")
	groupFallback := s.cacheProviderSessionScope(PlatformAnthropic, 11, 102, "same-content")
	c := history.send(groupFallback, 3, now.Add(3*time.Second), caps)
	require.Len(t, history.entries, 2)
	require.Empty(t, history.snapshot(groupFallback, c.owner, c.sentAt)[0].hashes)
	require.False(t, history.current(first, a.owner))
	require.False(t, history.remember(first, a.owner, chain, 9999, now.Add(4*time.Second), caps),
		"evicted worker must not recreate history")
	again := history.send(first, 4, now.Add(5*time.Second), caps)
	require.NotSame(t, a.owner, again.owner)
	require.False(t, history.remember(first, a.owner, chain, 9999, now.Add(6*time.Second), caps),
		"digest reuse must not publish into a replacement epoch")
	require.Empty(t, history.snapshot(first, again.owner, again.sentAt)[0].hashes)
}

func TestCacheProviderHistoryTimingAndRetry(t *testing.T) {
	key := cacheProviderSessionKey{platform: PlatformGemini, digest: "0123456789abcdef"}
	caps := CachePolicyCaps{MaxSessions: 2, ChainsPerSession: 3, MaxCheckpoints: 512}
	now := time.Now()
	var history cacheProviderTraceHistory
	first := history.send(key, 10, now, caps)
	gap, _ := history.settle(key, first, now.Add(time.Second))
	require.Equal(t, "no_prev", gap.source)
	second := history.send(key, 11, now.Add(2*time.Second), caps)
	// Retry is still a single logical request. Its send timestamp moves.
	second = history.send(key, 11, now.Add(3*time.Second), caps)
	gap, _ = history.settle(key, second, now.Add(4*time.Second))
	require.Equal(t, "settled", gap.source)
	require.EqualValues(t, 2000, gap.milliseconds)
	require.EqualValues(t, 11, second.owner.recent[0].sequence)
	require.EqualValues(t, 10, second.owner.recent[1].sequence)
	require.Zero(t, second.owner.recent[2].sequence)
	require.Zero(t, second.owner.chains, "closing timing does not train chain history")
}

func TestCacheProviderHistoryMissingSessionZeroWork(t *testing.T) {
	var history cacheProviderTraceHistory
	require.Zero(t, testing.AllocsPerRun(100, func() {
		token := history.send(cacheProviderSessionKey{}, 1, time.Time{}, CachePolicyCaps{})
		history.settle(cacheProviderSessionKey{}, token, time.Time{})
	}))
	require.Nil(t, history.entries)
	require.Zero(t, history.order.Len())
}

func TestCacheProviderHistoryConcurrentPublication(t *testing.T) {
	key := cacheProviderSessionKey{platform: PlatformGemini, digest: "0123456789abcdef"}
	var history cacheProviderTraceHistory
	caps := CachePolicyCaps{MaxSessions: 2, ChainsPerSession: 3, MaxCheckpoints: 512}
	now := time.Now()
	token := history.send(key, 1, now, caps)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := now.Add(time.Duration(i+1) * time.Millisecond)
			_, owner := history.settle(key, token, at)
			history.remember(key, owner, cachePrefixChain{hashes: []uint64{uint64(i)}, blocks: 1}, 2000+i, at, caps)
			_ = history.current(key, token.owner)
			_ = history.snapshot(key, token.owner, at)
		}(i)
	}
	wg.Wait()
	chains := history.snapshot(key, token.owner, now.Add(time.Second))
	for i, chain := range chains {
		require.Len(t, chain.hashes, 1)
		if i > 0 {
			require.False(t, chain.settledAt.After(chains[i-1].settledAt))
		}
	}
}
