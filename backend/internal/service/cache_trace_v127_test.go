package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"

	"golang.org/x/time/rate"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCachePrefixForkKindTable(t *testing.T) {
	items := make([]string, 2400)
	for i := range items {
		items[i] = fmt.Sprintf(`{"role":"user","content":"fork-private-%d"}`, i)
	}
	oldBody := prefixTestBody(items...)
	changed := func(index int) []byte {
		out := append([]string(nil), items...)
		out[index] = `{"role":"user","content":"changed-private"}`
		return prefixTestBody(out...)
	}
	shortChanged := append([]string(nil), items[:1500]...)
	shortChanged[1499] = `{"role":"user","content":"short-private-change"}`
	cases := []struct {
		name, kind, precision string
		body                  []byte
		history               bool
	}{
		{"no_history", "none", "none", oldBody, false},
		{"identical", "identical", "none", oldBody, true},
		{"append", "append", "none", prefixTestBody(append(append([]string(nil), items...), `{"role":"user","content":"new"}`)...), true},
		{"verified_cut", "cur_prefix", "exact", prefixTestBody(items[:61]...), true},
		{"unverified_cut", "tail_unproven", "range", prefixTestBody(items[:1500]...), true},
		{"changed_unverified_cut", "tail_unproven", "range", prefixTestBody(shortChanged...), true},
		{"dense_diverge", "diverge", "exact", changed(7), true},
		{"sparse_diverge", "diverge", "range", changed(1499), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hs [cachePrefixHistoryCount]cachePrefixChain
			if tc.history {
				hs[0], _, _ = prefixTestScan(oldBody)
				hs[0].settledAt = time.Now().Add(-time.Minute)
			}
			cur, pos, o := scanCachePrefix(tc.body, "responses", prefixTestKey, nil, hs)
			compareCachePrefix(cur, pos, hs, &o)
			require.Equal(t, tc.kind, o.ForkKind)
			require.Equal(t, tc.precision, o.ForkPrecision)
			switch tc.precision {
			case "none":
				require.Equal(t, []int{-1, -1, -1, -1}, []int{o.ForkBlock, o.ForkByte, o.ForkBlockHi, o.ForkByteHi})
			case "range":
				require.Greater(t, o.ForkBlockHi, o.ForkBlock)
				require.Greater(t, o.ForkByteHi, o.ForkByte)
			}
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			trace := &cacheTrace{data: cacheTraceData{Prefix: o}, chain: cur}
			trace.emitCacheTrace()
			slog.SetDefault(previous)
			var event map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &event))
			require.Equal(t, tc.kind, event["fork_kind"])
			require.Equal(t, tc.precision, event["fork_precision"])
			require.NotContains(t, output.String(), "private")
			for _, h := range append(append([]uint64(nil), cur.hashes...), cur.final) {
				require.NotContains(t, output.String(), strconv.FormatUint(h, 10))
				require.NotContains(t, output.String(), fmt.Sprintf("%016x", h))
			}
		})
	}
}

func TestCachePrefixThirdHistoryProofOutranksRecentRanges(t *testing.T) {
	items := make([]string, 2400)
	for i := range items {
		items[i] = fmt.Sprintf(`{"role":"user","content":"%d"}`, i)
	}
	var hs [cachePrefixHistoryCount]cachePrefixChain
	for i, n := range []int{2400, 2300, 1500} {
		hs[i], _, _ = prefixTestScan(prefixTestBody(items[:n]...))
		hs[i].settledAt = time.Now().Add(-time.Duration(i+1) * time.Minute)
		hs[i].prompt = 1000 + i
	}
	cur, pos, o := scanCachePrefix(prefixTestBody(items[:1500]...), "responses", prefixTestKey, nil, hs)
	compareCachePrefix(cur, pos, hs, &o)
	require.Equal(t, "identical", o.ForkKind)
	require.Equal(t, "none", o.ForkPrecision)
	require.Equal(t, "prevk", o.ForkRef)
	require.Equal(t, 1002, o.PreviousPrompt)
}

func TestCacheTraceByteRateDefaults(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{}}
	pool := s.cacheTraceWorkerPool()
	require.Equal(t, 1, pool.workers)
	require.Equal(t, int64(32<<20), pool.bytesPerSecond)
	require.Equal(t, int64(64<<20), pool.budget)
}

func TestCacheTraceWorkerWaitsForRateTokens(t *testing.T) {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-secret"}}
	svc := &OpenAIGatewayService{cfg: cfg}
	traceTestConfig(svc)
	limiter := rate.NewLimiter(128, 4096)
	require.True(t, limiter.AllowN(time.Now(), 4096))
	pool := &cacheTraceWorkers{budget: 4096, workers: 1, bytesPerSecond: 128, limiter: limiter}
	state := &cacheTraceAsync{}
	body := []byte(`{"input":"synthetic","prompt_cache_key":"synthetic"}`)
	opened := make(chan struct{})
	job := &cacheTraceJob{state: state, service: svc, policy: svc.cachePolicies.Snapshot(), ingress: body, size: int64(len(body)), created: time.Now(), session: "wait", protocol: "responses", transport: "http",
		getBody: func() (io.ReadCloser, error) { close(opened); return io.NopCloser(bytes.NewReader(body)), nil }}
	require.True(t, pool.submit(job), "rate exhaustion must never reject admission")
	require.Eventually(t, func() bool { return limiter.Tokens() < 0 }, time.Second, time.Millisecond, "worker should reserve tokens and wait")
	pool.mu.Lock()
	retained := pool.bytes
	pool.mu.Unlock()
	require.Equal(t, int64(3*len(body)+1024), retained, "waiting work remains within the byte budget")
	require.Nil(t, state.result.Load())
	snapshot := &cacheTrace{async: state, data: cacheTraceData{Prefix: cachePrefixUnavailable(len(body))}}
	snapshot.applyCacheTraceComputed()
	require.False(t, snapshot.data.Prefix.Sampled, "response snapshot does not wait for the worker")
	require.Eventually(t, func() bool { return state.result.Load() != nil }, 3*time.Second, time.Millisecond)
	<-opened
	require.Eventually(t, func() bool { pool.mu.Lock(); defer pool.mu.Unlock(); return pool.bytes == 0 && pool.active == 0 }, time.Second, time.Millisecond)
}

func TestCacheTraceRateWaitingExpiresWithoutReading(t *testing.T) {
	limiter := rate.NewLimiter(1, 4096)
	require.True(t, limiter.AllowN(time.Now(), 4096))
	p := &cacheTraceWorkers{budget: 4096, workers: 1, limiter: limiter}
	state := &cacheTraceAsync{}
	require.True(t, p.submit(&cacheTraceJob{state: state, queuedAt: time.Now().Add(-cacheTraceDefaultQueueWait + 100*time.Millisecond), size: 100,
		getBody: func() (io.ReadCloser, error) {
			t.Error("deadline cannot fit token wait; must not read")
			return io.NopCloser(strings.NewReader("{}")), nil
		}}))
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.bytes == 0 && p.active == 0 }, time.Second, time.Millisecond)
	require.Nil(t, state.result.Load())
}

func TestCacheTraceBudgetRejectsWithoutReadingOrOverflow(t *testing.T) {
	p := &cacheTraceWorkers{budget: 64 << 20}
	state := &cacheTraceAsync{}
	j := &cacheTraceJob{state: state, ingress: make([]byte, 100), created: time.Now(),
		getBody: func() (io.ReadCloser, error) { t.Fatal("rejected body read"); return nil, nil }}
	for _, size := range []int64{-1, math.MaxInt64, 32 << 20} {
		j.size = size
		require.False(t, p.submit(j))
		require.Zero(t, p.bytes)
		require.Nil(t, p.head)
		require.Nil(t, state.result.Load())
	}
}

func TestCacheTraceRateConfiguration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.CacheTrace.Workers = 2
	svc := &OpenAIGatewayService{cfg: cfg}
	traceTestConfig(svc)
	traceTestSetPolicy(svc, map[string]any{"byte_budget_mib": 32, "rate_mib_per_s": 8})
	p := svc.cacheTraceWorkerPool()
	require.Equal(t, 2, p.workers)
	require.Equal(t, int64(32<<20), p.budget)
	require.Equal(t, int64(8<<20), p.bytesPerSecond)
	traceTestSetPolicy(svc, map[string]any{"byte_budget_mib": 16, "rate_mib_per_s": 4})
	require.Same(t, p, svc.cacheTraceWorkerPool())
	require.Equal(t, int64(16<<20), p.budget)
	require.Equal(t, int64(4<<20), p.bytesPerSecond)
}

func TestCacheTraceRateCannotDiscardBeforeActualExpiry(t *testing.T) {
	limiter := rate.NewLimiter(1, 4096)
	require.True(t, limiter.AllowN(time.Now(), 4096))
	p := &cacheTraceWorkers{budget: 4096, workers: 1, limiter: limiter}
	deadline := time.Now().Add(100 * time.Millisecond)
	require.True(t, p.submit(&cacheTraceJob{state: &cacheTraceAsync{}, queuedAt: deadline.Add(-cacheTraceDefaultQueueWait), size: 100,
		getBody: func() (io.ReadCloser, error) {
			t.Error("expired job must not read body")
			return io.NopCloser(strings.NewReader("{}")), nil
		}}))
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.active == 0 }, time.Second, time.Millisecond)
	require.False(t, time.Now().Before(deadline), "predicted token shortage must retain task until actual expiry")
}
