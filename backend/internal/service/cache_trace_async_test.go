package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Tests may wait to inspect asynchronous fields. No production response or
// settlement calls this helper; separate tests deliberately block the worker.
func awaitCacheTrace(t *testing.T, s *OpenAIGatewayService, c *gin.Context, result *OpenAIForwardResult) {
	t.Helper()
	tr := cacheTraceFromGin(c)
	if tr == nil {
		return
	}
	s.startCacheTraceRequest(c) // builder-only fixtures simulate the send boundary
	if tr.async == nil {
		return
	}
	require.Eventually(t, func() bool { return tr.async.result.Load() != nil }, 5*time.Second, time.Millisecond)
	tr.mu.Lock()
	tr.applyCacheTraceComputed()
	tr.mu.Unlock()
	if result != nil && result.CacheTrace != nil {
		result.CacheTrace.mu.Lock()
		result.CacheTrace.applyCacheTraceComputed()
		result.CacheTrace.mu.Unlock()
	}
}

func TestCachePrefixSparseLongSequences(t *testing.T) {
	items := make([]string, 2400)
	for i := range items {
		items[i] = fmt.Sprintf(`{"role":"user","content":"%d"}`, i)
	}
	oldBody := prefixTestBody(items...)
	old, _, _ := prefixTestScan(oldBody)
	require.LessOrEqual(t, len(old.hashes), 512)
	require.LessOrEqual(t, cap(old.hashes)*8, 4096)
	require.Equal(t, len(items)+3, old.blocks)
	for _, extra := range []int{0, 1, 2500} {
		next := append([]string{}, items...)
		for i := 0; i < extra; i++ {
			next = append(next, `{"role":"assistant","content":"append"}`)
		}
		o := prefixTestCompare(oldBody, prefixTestBody(next...))
		require.Equal(t, "none", o.ForkPrecision)
		require.Equal(t, -1, o.ForkBlock)
		require.False(t, o.TruncatedUnknown)
	}
	for _, block := range []int{3, 20, 63, 64, 500, 600, 1500, 2402} {
		next := append([]string{}, items...)
		next[block-3] = `{"role":"user","content":"changed"}`
		o := prefixTestCompare(oldBody, prefixTestBody(next...))
		if block < 64 {
			require.Equal(t, "exact", o.ForkPrecision)
			require.Equal(t, block, o.ForkBlock)
		} else {
			require.LessOrEqual(t, o.ForkBlock, block)
			hi := o.ForkBlock
			if o.ForkPrecision == "range" {
				hi = o.ForkBlockHi
				require.GreaterOrEqual(t, o.ForkByteHi, o.ForkByte)
			}
			require.GreaterOrEqual(t, hi, block)
			require.Less(t, hi-o.ForkBlock, old.stride)
		}
	}
	// Truncate on a stored checkpoint: the entire shorter prefix is proven.
	o := prefixTestCompare(oldBody, prefixTestBody(items[:61]...))
	require.Equal(t, "exact", o.ForkPrecision)
	require.Equal(t, 64, o.ForkBlock)
	// Between sparse checkpoints we cannot prove the unobserved tail equal.
	// Keep a bounded range rather than inventing an exact match.
	o = prefixTestCompare(oldBody, prefixTestBody(items[:1500]...))
	require.Equal(t, "range", o.ForkPrecision)
	require.LessOrEqual(t, o.ForkBlock, 1503)
	// A recent long history cannot verify a shortened tail between checkpoints.
	// An older history ending at this exact length proves equality completely.
	shortened := prefixTestBody(items[:1500]...)
	older, _, _ := prefixTestScan(shortened)
	older.prompt = 4321
	older.anchor = "same"
	older.settledAt = time.Now().Add(-time.Minute)
	old.anchor = "same"
	old.prompt = 9876
	old.settledAt = time.Now()
	hs := [3]cachePrefixChain{old, older}
	shortenedChain, shortenedPos, shortenedObs := scanCachePrefix(shortened, "responses", prefixTestKey, nil, hs)
	shortenedChain.anchor = "same"
	compareCachePrefix(shortenedChain, shortenedPos, hs, &shortenedObs)
	require.Equal(t, "prevk", shortenedObs.ForkRef)
	require.Equal(t, 4321, shortenedObs.PreviousPrompt)
	require.Equal(t, "none", shortenedObs.ForkPrecision, "identical older chain is a fully proven candidate")
	modified := append([]string{}, items[:1500]...)
	modified[1499] = `{"role":"user","content":"modified tail"}`
	changedTail := prefixTestCompare(oldBody, prefixTestBody(modified...))
	require.Equal(t, "range", changedTail.ForkPrecision)
	require.LessOrEqual(t, changedTail.ForkBlock, 1502)
	require.Equal(t, 1503, changedTail.ForkBlockHi)
	// Interleaved histories: choose the longest proven prefix, not latest arrival.
	alternate := append([]string{}, items...)
	alternate[1000] = `{"role":"user","content":"other"}`
	other, _, _ := prefixTestScan(prefixTestBody(alternate...))
	other.anchor = "other"
	old.anchor = "same"
	old.prompt = 12345
	old.settledAt = time.Now().Add(-5 * time.Second)
	history := [3]cachePrefixChain{other, old}
	cur, pos, ob := scanCachePrefix(prefixTestBody(append(items, `{"role":"user","content":"new"}`)...), "responses", prefixTestKey, nil, history)
	cur.anchor = "same"
	compareCachePrefix(cur, pos, history, &ob)
	require.Equal(t, "prevk", ob.ForkRef)
	require.Equal(t, "none", ob.ForkPrecision)
	require.Equal(t, 12345, ob.PreviousPrompt)
	require.GreaterOrEqual(t, ob.HistoryAgeSeconds, 5)
}

func TestCacheTraceAsyncBudgetAndNonblockingSnapshot(t *testing.T) {
	s, c, req, body := cacheTraceWireFixture(t, "responses", "apikey", "none", "none", traceTestConfig)
	// The wire fixture has already awaited its worker. Start a fresh trace so
	// the assertions below actually exercise an unobserved incoming body.
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, (*cacheTrace)(nil)))
	s.beginCacheTrace(c, body)
	originalGet := req.GetBody
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	req.GetBody = func() (io.ReadCloser, error) { close(entered); <-release; return originalGet() }
	resetCacheTraceAttempt(c)
	s.captureCacheTraceRequest(c, req, &Account{ID: 1, Platform: PlatformOpenAI}, "responses")
	pool := s.cacheTraceWorkerPool()
	s.startCacheTraceRequest(c)
	<-entered
	result := &OpenAIForwardResult{}
	s.finishCacheTrace(c, result)
	require.NotNil(t, result.CacheTrace)
	require.False(t, result.CacheTrace.data.Prefix.Sampled, "snapshot must not wait for a blocked worker")
	require.Equal(t, "unknown", result.CacheTrace.data.PCKClassIn)
	require.Equal(t, "unknown", result.CacheTrace.data.PCKClassOut, "no body evidence or injection marker yet")
	// Budget includes active work; a queue with an occupied worker cannot exceed it.
	pool.mu.Lock()
	pool.budget = pool.bytes
	pool.mu.Unlock()
	j := &cacheTraceJob{size: 1, created: time.Now()}
	require.False(t, pool.submit(j))
	unblock()
	awaitCacheTrace(t, s, c, result)
	require.True(t, result.CacheTrace.data.Prefix.Sampled)
}

func TestCacheTraceAsyncRetryImmutableAndLateHistory(t *testing.T) {
	s, c, req, wire := cacheTraceWireFixture(t, "responses", "apikey", "none", "none", traceTestConfig)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		// Independent request state but shared worker and history: retries reset
		// the live trace without touching any previous attempt's immutable job.
		local := *c.Request
		cc := &gin.Context{Request: &local}
		first := &cacheTrace{sessionKey: "retry", ingress: wire, apiKeyID: 1, data: cacheTraceData{Transport: "http"}}
		cc.Request = cc.Request.WithContext(context.WithValue(cc.Request.Context(), cacheTraceKey{}, first))
		snapshotReq := req.Clone(req.Context())
		original := snapshotReq.GetBody
		ready := make(chan struct{})
		snapshotReq.GetBody = func() (io.ReadCloser, error) { <-ready; return original() }
		s.captureCacheTraceRequest(cc, snapshotReq, &Account{ID: 1, Platform: PlatformOpenAI}, "responses")
		s.startCacheTraceRequest(cc)
		result := &OpenAIForwardResult{}
		s.finishCacheTrace(cc, result)
		state := result.CacheTrace.async
		resetCacheTraceAttempt(cc)
		// Request-local mutation is assignment to new bytes/readers, never an
		// in-place rewrite of the snapshot's body or a pooled buffer return.
		snapshotReq.Body = io.NopCloser(strings.NewReader(`{"input":"replacement"}`))
		snapshotReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader([]byte(`{}`))), nil }
		telemetry := s.cacheTelemetry()
		telemetry.mu.Lock()
		previous := telemetry.sessions["retry"]
		previous.at = time.Now()
		telemetry.sessions["retry"] = previous
		telemetry.mu.Unlock()
		state.settled.Store(&cacheTraceSettlement{prompt: 9000, at: time.Now()})
		close(ready)
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(3 * time.Second)
			for state.result.Load() == nil {
				if time.Now().After(deadline) {
					t.Errorf("retry chain did not complete: reason=%s", state.naReason())
					return
				}
				time.Sleep(time.Millisecond)
			}
			state.publish(s, "retry")
		}()
	}
	wg.Wait()
	telemetry := s.cacheTelemetry()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	require.NotNil(t, telemetry.sessions["retry"].traceChains)
	require.Equal(t, 9000, telemetry.sessions["retry"].traceChains[0].prompt)
}

func TestCacheTraceAsyncDoesNotCompareFutureSettlement(t *testing.T) {
	s, _, _, body := cacheTraceWireFixture(t, "responses", "apikey", "none", "none", traceTestConfig)
	before := time.Now()
	chain, _, _ := prefixTestScan(body)
	chain.settledAt = before.Add(time.Second)
	telemetry := s.cacheTelemetry()
	telemetry.sessions["future"] = openAICacheHistory{traceChains: &[3]cachePrefixChain{chain}}
	got := s.computeCacheTrace(body, body, 1, false, "future", "responses", "http", before)
	require.True(t, got.prefix.HistoryUnsampled, "newer settlement cannot explain an earlier request")
	require.Equal(t, "none", got.prefix.ForkRef)
	chain.anchor = got.anchor
	future := chain
	chain.settledAt = before.Add(-time.Second)
	telemetry.sessions["future"] = openAICacheHistory{traceChains: &[3]cachePrefixChain{future, chain}}
	got = s.computeCacheTrace(body, body, 1, false, "future", "responses", "http", before)
	require.False(t, got.prefix.HistoryUnsampled)
	require.Equal(t, "prev1", got.prefix.ForkRef)
}

func TestCacheTraceAsyncFailureReleasesBudget(t *testing.T) {
	for _, kind := range []string{"expired", "reader_error", "reader_panic"} {
		t.Run(kind, func(t *testing.T) {
			state := &cacheTraceAsync{}
			created := time.Now()
			if kind == "expired" {
				created = created.Add(-2 * cacheTraceAsyncLifetime)
			}
			p := &cacheTraceWorkers{budget: 4096, workers: 1}
			require.True(t, p.submit(&cacheTraceJob{
				state: state, created: created, queuedAt: created, size: 1,
				getBody: func() (io.ReadCloser, error) {
					if kind == "reader_panic" {
						panic("synthetic")
					}
					if kind == "expired" {
						t.Error("expired body must not be opened")
					}
					return nil, fmt.Errorf("synthetic")
				},
			}))
			require.Eventually(t, func() bool {
				p.mu.Lock()
				defer p.mu.Unlock()
				return p.bytes == 0 && p.active == 0
			}, time.Second, time.Millisecond)
			require.Nil(t, state.result.Load())
		})
	}
}
