//go:build unit

package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newCacheTraceL01Fixture(t *testing.T, enabled bool) (*OpenAIGatewayService, *gin.Context, *cacheTrace) {
	t.Helper()
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-l01-trace-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	policies := NewCachePolicyService(nil, cfg)
	policies.publish(map[string]string{
		cachePolicySettings[0]: fmt.Sprintf(`{"rev":1,"spec":{"enabled":%t}}`, enabled),
		cachePolicySettings[1]: `{"rev":1,"spec":{"enabled":true}}`,
		cachePolicySettings[3]: `{"rev":7,"spec":{"mode":"uuid_fill","rollout_percent":100}}`,
	})
	s := &OpenAIGatewayService{cfg: cfg, cachePolicies: policies}
	c := cacheTestContext(1, 10)
	body := []byte(`{"input":[{"role":"user","content":"PRIVATE_L01_TRACE_BODY"}],"prompt_cache_key":"9c912cac-bcc7-4451-924a-bf186c4b49dd"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("X-Client-Request-Id", "PRIVATE_L01_TRACE_HEADER")
	c.Set(openAICacheSessionGinKey, "v2:0011223344556677")
	c.Set(openAICacheSessionProvenanceGinKey, true)
	s.beginCacheTrace(c, body)
	trace := cacheTraceFromGin(c)
	require.NotNil(t, trace)
	return s, c, trace
}

func TestCacheTraceL01SourceEnums(t *testing.T) {
	trace := &cacheTrace{}
	for _, source := range []string{"none", "passthrough", "gw_compat_hex", "gw_from_header", "gw_grok", "oauth_rewritten", "gw_uuid"} {
		trace.setPCKSource(source)
		require.Equal(t, source, trace.data.PCKClassOut)
	}
	for _, source := range []string{"none", "client_value", "uuid_of_pck", "gw_isolated", "fingerprint_converged", "eq_pck"} {
		trace.setSessionSource(source)
		require.Equal(t, source, trace.data.SessOutRelation)
	}
	const privateUUID = "9c912cac-bcc7-4451-924a-bf186c4b49dd"
	trace.setPCKSource(privateUUID)
	trace.setSessionSource(privateUUID)
	require.Equal(t, "gw_uuid", trace.data.PCKClassOut)
	require.Equal(t, "eq_pck", trace.data.SessOutRelation)
	var absent *cacheTrace
	require.NotPanics(t, func() { absent.setPCKSource("gw_uuid"); absent.setSessionSource("eq_pck") })
}

func TestCacheTraceL01BeginAndAttemptReset(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			s, c, trace := newCacheTraceL01Fixture(t, enabled)
			wantSkip := "policy_off"
			if enabled {
				wantSkip = "unsupported_ingress"
			}
			require.Equal(t, "none", trace.data.L01Arm)
			require.Equal(t, wantSkip, trace.data.L01Skip)
			require.False(t, trace.data.L01Applied)
			require.EqualValues(t, 7, trace.data.L01PolicyRevision)
			recordOpenAIUpstreamIdentity(c, openAIUpstreamIdentityDecision{Arm: "on", PolicyRevision: 7}, true, "none")
			trace.setPCKSource("gw_uuid")
			trace.setSessionSource("eq_pck")
			trace.mu.Lock()
			trace.data.AccountID, trace.data.ProtocolOut = 9, "responses"
			trace.mu.Unlock()
			result := &OpenAIForwardResult{RequestID: "PRIVATE_L01_REQUEST_ID"}
			s.finishCacheTrace(c, result)
			require.NotNil(t, result.CacheTrace)
			resetCacheTraceAttempt(c)
			require.Equal(t, "none", trace.data.L01Arm)
			require.Equal(t, wantSkip, trace.data.L01Skip)
			require.False(t, trace.data.L01Applied)
			require.EqualValues(t, 7, trace.data.L01PolicyRevision)
			require.NotEqual(t, "gw_uuid", trace.data.PCKClassOut)
			require.Equal(t, "none", trace.data.SessOutRelation)
			require.True(t, result.CacheTrace.data.L01Applied, "settlement owns the prior immutable attempt")
			require.Equal(t, "on", result.CacheTrace.data.L01Arm)
			// Scheduling can revisit the same trace after a serving-group
			// change; that must also discard a failed attempt's source state.
			recordOpenAIUpstreamIdentity(c, openAIUpstreamIdentityDecision{Arm: "off", PolicyRevision: 7}, false, "arm_off")
			s.beginCacheTrace(c, []byte("PRIVATE_L01_UNPARSED_RETRY"))
			require.Same(t, trace, cacheTraceFromGin(c))
			require.Equal(t, "none", trace.data.L01Arm)
			require.Equal(t, wantSkip, trace.data.L01Skip)
			require.False(t, trace.data.L01Applied)
		})
	}
}

func TestCacheTraceL01RetryUsesFrozenPolicy(t *testing.T) {
	s, c, trace := newCacheTraceL01Fixture(t, true)
	frozen := trace.policy
	s.cachePolicies.publish(map[string]string{
		cachePolicySettings[1]: `{"rev":2,"spec":{"enabled":true}}`,
		cachePolicySettings[3]: `{"rev":8,"spec":{"mode":"off"}}`,
	})
	require.False(t, s.cachePolicies.Snapshot().UpstreamIdentity().Enabled())
	resetCacheTraceAttempt(c)
	require.Same(t, frozen, trace.policy)
	require.Equal(t, "unsupported_ingress", trace.data.L01Skip)
	require.EqualValues(t, 7, trace.data.L01PolicyRevision)
	s.beginCacheTrace(c, nil)
	require.Equal(t, "unsupported_ingress", trace.data.L01Skip)
	require.EqualValues(t, 7, trace.data.L01PolicyRevision)
}

func TestCacheTraceL01FullEventAndPrivacy(t *testing.T) {
	_, c, trace := newCacheTraceL01Fixture(t, true)
	logs := t3SettlementCapture(t)
	recordOpenAIUpstreamIdentity(c, openAIUpstreamIdentityDecision{Arm: "on", PolicyRevision: 7}, true, "none")
	trace.setPCKSource("gw_uuid")
	trace.setSessionSource("eq_pck")
	trace.data.RequestIDHMAC = strings.Repeat("a", 64)
	trace.data.LossClass = "full"
	trace.data.PromptTokens, trace.data.ReadTokens = 4096, 4096
	trace.settled = true
	trace.emitCacheTrace()
	trace.emitCacheTrace()
	events := t3SettlementEvents(t, logs, "cache_trace")
	require.Len(t, events, 1)
	payload := t3SettlementPayload(t, events[0], false, "m1.o1.u7.a0.r0")
	require.Equal(t, "on", payload["l01_arm"])
	require.Equal(t, true, payload["l01_applied"])
	require.Equal(t, "none", payload["l01_skip"])
	require.Equal(t, float64(7), payload["l01_policy_rev"])
	require.Equal(t, "gw_uuid", payload["pck_class_out"])
	require.Equal(t, "eq_pck", payload["sess_out_relation"])
	for _, private := range []string{"9c912cac-bcc7-4451-924a-bf186c4b49dd", "PRIVATE_L01_TRACE_BODY", "PRIVATE_L01_TRACE_HEADER", "PRIVATE_L01_REQUEST_ID", "synthetic-l01-trace-secret"} {
		require.NotContains(t, logs.String(), private)
	}
}

func TestCacheTraceL01EmitDefaultsAndMinimalShape(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("full_default_%t", enabled), func(t *testing.T) {
			_, _, started := newCacheTraceL01Fixture(t, enabled)
			logs := t3SettlementCapture(t)
			trace := &cacheTrace{policy: started.policy, settled: true, data: cacheTraceData{LossClass: "full", RequestIDHMAC: strings.Repeat("b", 64), SessionDigest: "v2:0011223344556677"}}
			trace.emitCacheTrace()
			events := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, events, 1)
			require.Equal(t, "none", events[0]["l01_arm"])
			require.Equal(t, false, events[0]["l01_applied"])
			want := "policy_off"
			if enabled {
				want = "unsupported_ingress"
			}
			require.Equal(t, want, events[0]["l01_skip"])
			require.Equal(t, float64(7), events[0]["l01_policy_rev"])
		})
	}
	for _, reason := range []string{"budget_full", "expired", "not_ready", "observer_failed"} {
		t.Run("minimal_"+reason, func(t *testing.T) {
			_, _, started := newCacheTraceL01Fixture(t, true)
			logs := t3SettlementCapture(t)
			trace := &cacheTrace{policy: started.policy, settled: true, data: cacheTraceData{
				LossClass: "n/a_sampled_out", NAReason: reason, Transport: "http",
				RequestIDHMAC: strings.Repeat("c", 64), SessionDigest: "v2:0011223344556677",
				L01Arm: "on", L01Applied: true, L01Skip: "none", L01PolicyRevision: 7,
			}}
			trace.emitCacheTrace()
			events := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, events, 1)
			payload := t3SettlementPayload(t, events[0], true, "m0.o0.u0.a0.r0")
			require.Len(t, payload, 12)
			for _, field := range []string{"l01_arm", "l01_applied", "l01_skip", "l01_policy_rev"} {
				require.NotContains(t, payload, field)
			}
		})
	}
}

func TestCacheTraceL01UUIDFactDoesNotWaitForWorker(t *testing.T) {
	_, c, trace := newCacheTraceL01Fixture(t, true)
	recordOpenAIUpstreamIdentity(c, openAIUpstreamIdentityDecision{Arm: "on", PolicyRevision: 7}, true, "none")
	require.True(t, trace.data.PCKOutIsUUID)
	trace.async = &cacheTraceAsync{chainExcluded: true}
	trace.applyCacheTraceComputed()
	require.True(t, trace.data.PCKOutIsUUID)
	trace.async.result.Store(&cacheTraceComputed{})
	trace.applyCacheTraceComputed()
	require.True(t, trace.data.PCKOutIsUUID)
	require.Equal(t, "gw_uuid", trace.data.PCKClassOut)
}
