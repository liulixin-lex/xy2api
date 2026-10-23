package service

import (
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// An empty observation extension must not force each legacy session entry into
// its own heap object. This catches accidental growth past Go's map inline-value
// limit even though all trace processing functions remain gated off.
func TestCacheTraceDisabledSessionInsertionDoesNotAllocate(t *testing.T) {
	entries := make(map[string]openAICacheHistory, 1)
	allocations := testing.AllocsPerRun(1000, func() {
		delete(entries, "session")
		entries["session"] = openAICacheHistory{}
	})
	require.Zero(t, allocations)
	require.Nil(t, entries["session"].traceChains)
}

func TestCacheTraceConcurrentHistorySnapshots(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.CacheTrace.ChainPercent = 100
	svc := &OpenAIGatewayService{cfg: cfg}
	const session = "synthetic-concurrent-history"
	result := &OpenAIForwardResult{CacheDiagnostic: &OpenAICacheDiagnostic{Session: session}}
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	svc.observeCacheUsage(result, account, time.Now())
	body := prefixTestBody(`{"role":"user","content":"synthetic-history"}`)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			trace := &cacheTrace{sessionKey: session, data: cacheTraceData{Transport: "http", ConvAnchor: "synthetic-anchor"}}
			for range 40 {
				svc.captureCachePrefix(trace, body, "responses")
				svc.rememberCachePrefix(session, trace.chain, 2048)
				svc.observeCacheUsage(result, account, time.Now())
			}
		}()
	}
	close(start)
	workers.Wait()
	trace := &cacheTrace{sessionKey: session, data: cacheTraceData{Transport: "http", ConvAnchor: "synthetic-anchor"}}
	svc.captureCachePrefix(trace, body, "responses")
	require.Equal(t, "prev1", trace.data.Prefix.ForkRef)
	require.Equal(t, 2048, trace.data.Prefix.PreviousPrompt)
}
