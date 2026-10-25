//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestCachePolicyGatewayFreezesRetriesAndAsyncWork(t *testing.T) {
	ctx := context.Background()
	cfg := policyTestConfig()
	cfg.JWT.Secret = "synthetic-runtime-policy-secret"
	policies := NewCachePolicyService(newPolicyTestStore(), cfg)
	_, _, err := policies.Update(ctx, "observability", policyWrite(t,
		`{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	s, c, req, _ := cacheTraceWireFixture(t, "responses", "apikey", "none", "session_id",
		func(s *OpenAIGatewayService) { s.cachePolicies = policies })
	trace := cacheTraceFromGin(c)
	require.NotNil(t, trace)
	frozen := trace.policy
	require.Equal(t, "m0.o1.u0.a0.r0", trace.data.PolRev)
	_, _, err = policies.Update(ctx, "observability", policyWrite(t,
		`{"expected_rev":1,"spec":{"enabled":false,"chain_percent":0}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.False(t, policies.Snapshot().TraceEnabled())
	resetCacheTraceAttempt(c)
	s.beginCacheTrace(c, []byte(`{"input":"retry"}`))
	require.Same(t, trace, cacheTraceFromGin(c))
	require.Same(t, frozen, s.CaptureCachePolicy(c))
	s.captureCacheTraceRequest(c, req, &Account{ID: 9, Platform: PlatformOpenAI}, "responses")
	require.Same(t, frozen, trace.pending.policy)
	require.False(t, trace.async.chainExcluded, "the retry retains its original chain sampling")
	s.startCacheTraceRequest(c)
	awaitCacheTrace(t, s, c, nil)
	require.True(t, trace.data.Prefix.Sampled)
	require.Equal(t, "m0.o1.u0.a0.r0", trace.data.PolRev)

	next := cacheTestContext(1, 10)
	next.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	next.Set(openAICacheSessionGinKey, trace.sessionKey)
	s.beginCacheTrace(next, []byte(`{"input":"next"}`))
	require.Nil(t, cacheTraceFromGin(next), "next request observes the disabled policy")
	_, _, err = policies.Update(ctx, "observability", policyWrite(t,
		`{"expected_rev":2,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.False(t, s.CaptureCachePolicy(next).TraceEnabled())
	require.True(t, s.CaptureCachePolicyTurn(next).TraceEnabled())
	require.Same(t, frozen, trace.policy, "advancing another turn cannot mutate existing work")
}

func TestCachePolicyGatewayResourceUpdateKeepsPoolAndReservations(t *testing.T) {
	cfg := policyTestConfig()
	cfg.Gateway.CacheTrace.Workers = 2
	policies := NewCachePolicyService(newPolicyTestStore(), cfg)
	s := &OpenAIGatewayService{cfg: cfg, cachePolicies: policies}
	p := s.cacheTraceWorkerPool()
	p.limiter = rate.NewLimiter(rate.Limit(32<<20), 64<<20)
	old := policies.Snapshot()
	cost, ok := p.reserve(16<<20, 8<<20)
	require.True(t, ok)
	_, _, err := policies.Update(context.Background(), "observability", policyWrite(t,
		`{"expected_rev":0,"spec":{"byte_budget_mib":8,"rate_mib_per_s":1}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Same(t, p, s.cacheTraceWorkerPool())
	require.EqualValues(t, cost, p.reserved.Load(), "already admitted work retains ownership")
	_, ok = p.reserve(1, 1)
	require.False(t, ok)
	require.Equal(t, rate.Limit(1<<20), p.limiter.Limit())
	require.EqualValues(t, 64<<20, old.ByteBudget(), "resource publication does not mutate request snapshots")
	p.reserved.Add(-cost)
	cost, ok = p.reserve(1024, 1024)
	require.True(t, ok)
	p.reserved.Add(-cost)
	require.Equal(t, 2, p.workers)
}

func TestCachePolicyGatewayBudgetShrinkBeforeFirstWorker(t *testing.T) {
	policies := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	s := &OpenAIGatewayService{cfg: policyTestConfig(), cachePolicies: policies}
	pool := s.cacheTraceWorkerPool()
	cost, ok := pool.reserve(0, 9<<20)
	require.True(t, ok)
	require.Nil(t, pool.limiter)
	_, _, err := policies.Update(context.Background(), "observability", policyWrite(t,
		`{"expected_rev":0,"spec":{"byte_budget_mib":8}}`), nil, CachePolicyActor{})
	require.NoError(t, err)

	opened := make(chan struct{})
	job := &cacheTraceJob{pool: pool, cost: cost, size: 9 << 20,
		getBody: func() (io.ReadCloser, error) {
			close(opened)
			return nil, io.ErrUnexpectedEOF
		},
	}
	require.True(t, pool.submit(job))
	require.Eventually(t, func() bool { return pool.reserved.Load() == 0 }, 15*time.Second, time.Millisecond)
	select {
	case <-opened:
	default:
		t.Fatal("accepted work expired after a budget change before the first worker")
	}
}

func TestCachePolicyGatewayStaticHistoryCapsPreserveDiagnostics(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.CacheTrace.MaxSessions = 2
	cfg.Gateway.CacheTrace.ChainsPerSession = 1
	cfg.Gateway.CacheTrace.MaxCheckpoints = 4
	s := &OpenAIGatewayService{cfg: cfg, cachePolicies: NewCachePolicyService(nil, cfg)}
	telemetry := s.cacheTelemetry()
	body := prefixTestBody(strings.TrimSuffix(strings.Repeat(`{"role":"user","content":"synthetic"},`, 200), ","))
	chain, _, _ := scanCachePrefix(body, "responses", 1, nil)
	require.Greater(t, len(chain.hashes), 4)
	trace := &cacheTrace{policy: &CachePolicySnapshot{chainPercent: 100}, sessionKey: "pending"}
	s.captureCachePrefix(trace, body, "responses")
	require.Len(t, trace.chain.hashes, 4, "computed results obey the cap before settlement")
	for i := 0; i < 3; i++ {
		key := fmt.Sprint(i)
		telemetry.sessions[key] = openAICacheHistory{account: int64(i + 10), prefix: key, at: time.Unix(int64(i+1), 0)}
		s.rememberCachePrefix(key, chain, 2048)
	}
	require.Len(t, telemetry.sessions, 3, "diagnostics histories are never removed by the trace cap")
	require.Nil(t, telemetry.sessions["0"].traceChains)
	for _, key := range []string{"1", "2"} {
		entry := telemetry.sessions[key]
		require.Equal(t, key, entry.prefix)
		require.Len(t, entry.traceChains.chains[0].hashes, 4)
		require.Equal(t, chain.final, entry.traceChains.chains[0].final)
		require.Empty(t, entry.traceChains.chains[1].hashes)
		require.Empty(t, entry.traceChains.chains[2].hashes)
	}
}

func TestCachePolicyGatewayDisabledLayersHaveNoTraceOrAllocations(t *testing.T) {
	for _, mode := range []string{"default", "kill", "diagnostics", "sample_zero"} {
		t.Run(mode, func(t *testing.T) {
			cfg := policyTestConfig()
			cfg.JWT.Secret = "synthetic-runtime-secret"
			cfg.Gateway.CacheFeatures.KillSwitch = mode == "kill"
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = mode != "diagnostics"
			s := &OpenAIGatewayService{cfg: cfg}
			traceTestConfig(s)
			switch mode {
			case "default":
				s.cachePolicies = NewCachePolicyService(nil, cfg)
			case "sample_zero":
				traceTestSetPolicy(s, map[string]any{"sample_percent": 0})
			}
			c := cacheTestContext(1, 10)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set(openAICacheSessionGinKey, "synthetic-session")
			body := []byte("deliberately invalid JSON")
			require.Equal(t, 0.0, testing.AllocsPerRun(1000, func() {
				delete(c.Keys, cachePolicySnapshotGinKey)
				s.CaptureCachePolicy(c)
				s.beginCacheTrace(c, body)
			}))
			require.Nil(t, cacheTraceFromGin(c))
			require.Nil(t, s.cacheTraceWorkers)
		})
	}
}

func TestCachePolicyGatewaySamplingStableAcrossAttempts(t *testing.T) {
	cfg := policyTestConfig()
	cfg.JWT.Secret = "synthetic-sampling-secret"
	s := &OpenAIGatewayService{cfg: cfg}
	traceTestConfig(s)
	traceTestSetPolicy(s, map[string]any{"sample_percent": 50})
	selected, excluded := "", ""
	for i := 0; selected == "" || excluded == ""; i++ {
		session := fmt.Sprint(i)
		if sha256SumCacheCohort(session) < 50 {
			selected = session
		} else {
			excluded = session
		}
	}
	for _, first := range []string{selected, excluded} {
		c := cacheTestContext(1, 10)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set(openAICacheSessionGinKey, first)
		s.beginCacheTrace(c, []byte("{}"))
		initial := cacheTraceFromGin(c)
		next := selected
		if first == selected {
			next = excluded
			require.NotNil(t, initial)
		} else {
			require.Nil(t, initial)
		}
		c.Set(openAICacheSessionGinKey, next)
		s.beginCacheTrace(c, []byte("{}"))
		require.Same(t, initial, cacheTraceFromGin(c))
		if initial != nil {
			require.Equal(t, next, initial.data.SessionDigest)
		}
	}
}
