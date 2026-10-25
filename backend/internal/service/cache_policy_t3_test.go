//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCachePolicyT3DisabledEvents(t *testing.T) {
	previous := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(previous) })
	for _, mode := range []string{"disabled", "kill", "diagnostics", "sample_zero", "disabled_undelivered", "kill_undelivered", "diagnostics_undelivered", "sample_zero_undelivered"} {
		for _, noSession := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/no_session=%t/stream=%t", mode, noSession, stream), func(t *testing.T) {
					f := cacheTraceNewContractFixture(t, "runtime-off", "responses")
					gate := strings.TrimSuffix(mode, "_undelivered")
					billingContext := context.Background()
					var disconnect func()
					if gate != mode {
						client, cancel := context.WithCancel(context.Background())
						defer cancel()
						state := resilience.NewRequestState()
						state.InitDelivery(client, time.Now().Add(-time.Second), func() int64 { return 0 })
						state.SetAPIKey(f.api.ID)
						state.SetStage(resilience.StageHeaderWait)
						resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
						t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
						f.c.Request = f.c.Request.WithContext(resilience.WithRequestState(f.c.Request.Context(), state))
						billingContext = resilience.WithRequestState(context.Background(), state)
						disconnect = func() {
							cancel()
							disconnected, delivered, abuse := state.ResolveDisconnect()
							require.True(t, disconnected)
							require.False(t, delivered)
							require.True(t, abuse, "fixture must represent an undelivered request that is actually billed")
						}
					}
					f.s.cfg.Gateway.CacheFeatures.KillSwitch = gate == "kill"
					f.s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = gate != "diagnostics"
					f.s.cachePolicies = NewCachePolicyService(nil, f.s.cfg)
					if gate != "disabled" {
						traceTestSetPolicy(f.s, map[string]any{"enabled": true})
					}
					if gate == "sample_zero" {
						traceTestSetPolicy(f.s, map[string]any{"sample_percent": 0})
					}
					if noSession {
						f.body = []byte(cacheTraceNoSessionBody)
						f.c.Request.Header.Del("Session_id")
						f.pricingModel = "gpt-5.4"
					}
					if stream {
						f.body = bytes.Replace(f.body, []byte(`"stream":false`), []byte(`"stream":true`), 1)
					}
					var logs cacheTraceBillingPositiveLog
					old := slog.Default()
					slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
					t.Cleanup(func() { slog.SetDefault(old) })
					before := cacheTraceSendSequence.Load()
					upstream := &httpUpstreamRecorder{resp: traceForwardOK()}
					f.s.httpUpstream = upstream
					f.s.GenerateSessionHash(f.c, f.body)
					result, err := f.s.Forward(f.c.Request.Context(), f.c, f.account, f.body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1)
					require.Nil(t, cacheTraceFromGin(f.c))
					require.False(t, cacheTraceObserving(f.c))
					require.Nil(t, result.CacheTrace)
					require.Nil(t, result.cacheTraceFailure, "observer_failed must not acquire a sidecar while disabled")
					if disconnect != nil {
						disconnect()
					}
					require.NoError(t, f.s.RecordUsage(billingContext, &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account, OriginalModel: f.pricingModel}))
					require.Equal(t, 1, f.rows.calls)
					require.Equal(t, 1, f.billing.calls)
					require.Nil(t, f.s.cacheTraceWorkers, "neither admission nor rejection work may start")
					require.Equal(t, before, cacheTraceSendSequence.Load())
					telemetry := f.s.cacheTelemetry()
					telemetry.mu.Lock()
					for _, history := range telemetry.sessions {
						require.Nil(t, history.traceChains)
					}
					telemetry.mu.Unlock()
					require.Empty(t, cacheTraceBillingPositiveEvents(t, logs.snapshot(), "cache_trace"))
				})
			}
		}
	}
}

func TestCachePolicyT3ConcurrentToggleFreezesAttempts(t *testing.T) {
	cfg := policyTestConfig()
	cfg.JWT.Secret = "synthetic-runtime-toggle"
	policies := NewCachePolicyService(newPolicyTestStore(), cfg)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &OpenAIGatewayService{cfg: cfg, cachePolicies: policies}
			for i := 0; i < 100; i++ {
				c := cacheTestContext(1, 10)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Set(openAICacheSessionGinKey, "runtime-toggle-session")
				frozen := s.CaptureCachePolicy(c)
				s.beginCacheTrace(c, []byte("{}"))
				trace := cacheTraceFromGin(c)
				if !frozen.TraceEnabled() {
					if trace != nil {
						t.Error("disabled request constructed a trace")
					}
					continue
				}
				if trace == nil {
					t.Error("enabled request lost its frozen policy")
					continue
				}
				req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", bytes.NewReader([]byte("{}")))
				if err != nil {
					t.Error(err)
					continue
				}
				s.captureCacheTraceRequest(c, req, &Account{ID: 9, Platform: PlatformOpenAI}, "responses")
				trace.mu.Lock()
				trace.usageObservationFailed = i%2 == 0
				trace.mu.Unlock()
				result := &OpenAIForwardResult{RequestID: "synthetic-toggle"}
				s.finishCacheTrace(c, result)
				if i%2 == 0 {
					if result.cacheTraceFailure == nil || result.cacheTraceFailure.data.PolRev != frozen.Revision() {
						t.Error("failed observer changed policy revision")
					}
				} else if result.CacheTrace == nil || result.CacheTrace.policy != frozen || result.CacheTrace.data.PolRev != frozen.Revision() {
					t.Error("settlement snapshot changed policy revision")
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		rev := policies.Snapshot().ObservabilityRevision()
		_, _, err := policies.Update(context.Background(), "observability", policyWrite(t, fmt.Sprintf(`{"expected_rev":%d,"spec":{"enabled":%t}}`, rev, i%2 == 0)), nil, CachePolicyActor{})
		require.NoError(t, err)
	}
	wg.Wait()
}

func TestCachePolicyT3RefreshReachesGatewayWithin60Seconds(t *testing.T) {
	store := newPolicyTestStore()
	cfg := policyTestConfig()
	cfg.JWT.Secret = "synthetic-runtime-refresh"
	writer := NewCachePolicyService(store, cfg)
	reader := NewCachePolicyService(store, cfg)
	reader.Start()
	defer reader.Stop()
	gateway := &OpenAIGatewayService{cfg: cfg, cachePolicies: reader}
	for rev, enabled := range []bool{true, false} {
		_, _, err := writer.Update(context.Background(), "observability", policyWrite(t, fmt.Sprintf(`{"expected_rev":%d,"spec":{"enabled":%t}}`, rev, enabled)), nil, CachePolicyActor{})
		require.NoError(t, err)
		require.Equal(t, enabled, writer.Snapshot().TraceEnabled(), "local write is immediate")
		require.Eventually(t, func() bool { return reader.Snapshot().TraceEnabled() == enabled }, 60*time.Second, 25*time.Millisecond)
		c := cacheTestContext(1, 10)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		gateway.beginCacheTrace(c, []byte("{}"))
		require.Equal(t, enabled, cacheTraceFromGin(c) != nil)
		require.EqualValues(t, rev+1, CachePolicyFromGin(c).ObservabilityRevision())
	}
}

func TestCachePolicyT3TimingStateCapsPreserveDiagnostics(t *testing.T) {
	for _, atSend := range []bool{true, false} {
		t.Run(fmt.Sprintf("at_send=%t", atSend), func(t *testing.T) {
			cfg := policyTestConfig()
			cfg.JWT.Secret = "synthetic-runtime-timing-cap"
			cfg.Gateway.CacheTrace.MaxSessions = 2
			s := &OpenAIGatewayService{cfg: cfg}
			traceTestConfig(s)
			telemetry := s.cacheTelemetry()
			for i := 0; i < 3; i++ {
				key := fmt.Sprint(i)
				c := cacheTestContext(1, 10)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Set(openAICacheSessionGinKey, key)
				body := []byte("{}")
				s.beginCacheTrace(c, body)
				req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", bytes.NewReader(body))
				require.NoError(t, err)
				account := &Account{ID: 9, Platform: PlatformOpenAI}
				s.captureCacheTraceRequest(c, req, account, "responses")
				if atSend {
					telemetry.sessions[key] = openAICacheHistory{at: time.Unix(int64(i+1), 0)}
				}
				s.markCacheTraceSent(c)
				if !atSend {
					telemetry.sessions[key] = openAICacheHistory{at: time.Unix(int64(i+1), 0)}
				}
				result := &OpenAIForwardResult{RequestID: "synthetic-cap", CacheDiagnostic: &OpenAICacheDiagnostic{Session: key, GroupID: 10}}
				s.finishCacheTrace(c, result)
				s.settleCacheTrace(context.Background(), result, account)
			}
			require.Len(t, telemetry.sessions, 3)
			require.Nil(t, telemetry.sessions["0"].traceChains)
			require.NotNil(t, telemetry.sessions["1"].traceChains)
			require.NotNil(t, telemetry.sessions["2"].traceChains)
		})
	}
}
