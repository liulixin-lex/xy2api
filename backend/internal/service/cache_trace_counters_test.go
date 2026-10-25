//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCacheTraceCounterSettlementExactlyOnceInEitherOrder(t *testing.T) {
	for _, eventFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "event_first", false: "billing_first"}[eventFirst], func(t *testing.T) {
			policies := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
			_, _, err := policies.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
			require.NoError(t, err)
			p := policies.Snapshot()
			counters := NewCacheTraceCounters()
			s := &OpenAIGatewayService{cacheCounters: counters}
			result := &OpenAIForwardResult{cachePanel: &cachePanelRequest{policy: p}, Usage: OpenAIUsage{InputTokens: 2048, CacheReadInputTokens: 1024}}
			result.CacheTrace = &cacheTrace{policy: p, emitted: eventFirst, data: cacheTraceData{LossClass: "under_read", PromptTokens: 2048, ReadTokens: 1024, Prefix: cachePrefixObservation{PreviousPrompt: 2048}}}
			s.recordCachePanelTrace(result)
			s.recordCachePanelSettlement(result, &UsageLog{AccountID: 9}, &Account{ID: 9, Platform: PlatformOpenAI, Type: "oauth"})
			if !eventFirst {
				result.CacheTrace.emitted = true
			}
			s.recordCachePanelTrace(result)
			s.recordCachePanelTrace(result)
			s.recordCachePanelSettlement(result, &UsageLog{AccountID: 9}, &Account{ID: 9, Platform: PlatformOpenAI, Type: "oauth"})
			response := counters.Snapshot(time.Now(), CachePanelQuery{Window: "1h", Platform: "all"}, p)
			coverage := response["coverage"].(map[string]any)
			require.EqualValues(t, 1, coverage["business_requests"])
			require.EqualValues(t, 1, coverage["traced_requests"])
			require.InDelta(t, 1, *coverage["coverage_ratio"].(*float64), 1e-12)
		})
	}
}

func TestCacheTraceCountersConcurrentSnapshots(t *testing.T) {
	policies := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
	_, _, err := policies.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	counters := NewCacheTraceCounters()
	now := time.Now()
	data := cacheTraceData{LossClass: "under_read", PromptTokens: 2048, ReadTokens: 1024, PCKClassIn: "none", PCKClassOut: "none", SessOutRelation: "absent", Transport: "http", Prefix: cachePrefixObservation{PreviousPrompt: 2048}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				counters.RecordBusiness(now, 2048, 1024, "relay.example", "", "model", 9)
				counters.RecordTrace(now, data)
				_ = counters.Snapshot(now, CachePanelQuery{Window: "24h", Platform: "all"}, policies.Snapshot())
			}
		}()
	}
	wg.Wait()
	response := counters.Snapshot(now, CachePanelQuery{Window: "24h", Platform: "all"}, policies.Snapshot())
	coverage := response["coverage"].(map[string]any)
	require.EqualValues(t, 400, coverage["business_requests"])
	require.EqualValues(t, 400, coverage["traced_requests"])
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	for _, key := range []string{"api_key_id", "request_id_hmac", "session_digest", "user_agent"} {
		require.NotContains(t, string(raw), key)
	}
	_, _, err = policies.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":1,"spec":{"enabled":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	disabled := counters.Snapshot(now, CachePanelQuery{Window: "24h", Platform: "all"}, policies.Snapshot())
	require.Empty(t, disabled["loss_classes"])
	require.Nil(t, disabled["pck_out_is_uuid_ratio"])
	require.EqualValues(t, 0, disabled["coverage"].(map[string]any)["business_requests"])
}

func TestCacheTraceCountersDisabledForwardDoesNotCreateState(t *testing.T) {
	for _, mode := range []string{"off", "kill", "diagnostics"} {
		t.Run(mode, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "counter-off", "responses")
			f.s.cfg.Gateway.CacheFeatures.KillSwitch = mode == "kill"
			f.s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = mode != "diagnostics"
			f.s.cachePolicies = NewCachePolicyService(newPolicyTestStore(), f.s.cfg, WithCacheNorthstar())
			if mode != "off" {
				traceTestSetPolicy(f.s, map[string]any{"enabled": true})
			}
			f.s.cacheCounters = NewCacheTraceCounters()
			f.s.httpUpstream = &httpUpstreamRecorder{resp: traceForwardOK()}
			f.s.GenerateSessionHash(f.c, f.body)
			result, err := f.s.Forward(f.c.Request.Context(), f.c, f.account, f.body)
			require.NoError(t, err)
			require.Nil(t, result.cachePanel)
			require.NoError(t, f.s.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account, OriginalModel: f.pricingModel}))
			for _, bucket := range f.s.cacheCounters.minutes {
				require.Nil(t, bucket)
			}
			for _, bucket := range f.s.cacheCounters.hours {
				require.Nil(t, bucket)
			}
		})
	}
}

func TestCacheTraceCountersBusinessScopeAndHourlyIdempotency(t *testing.T) {
	policies := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
	_, _, err := policies.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	counters := NewCacheTraceCounters()
	s := &OpenAIGatewayService{cacheCounters: counters}
	result := &OpenAIForwardResult{cachePanel: &cachePanelRequest{policy: policies.Snapshot()}, Usage: OpenAIUsage{InputTokens: 2048}}
	s.recordCachePanelSettlement(result, &UsageLog{ImageCount: 1}, &Account{Platform: PlatformOpenAI})
	require.False(t, result.cachePanel.billed.Load())
	require.Empty(t, counters.PreviousHour(time.Now()))
	now := time.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	data := cacheTraceData{PromptTokens: 2048, ReadTokens: 512, LossClass: "under_read", Prefix: cachePrefixObservation{PreviousPrompt: 1024}}
	counters.RecordTrace(now, data)
	hours := counters.PreviousHour(now.Add(time.Hour))
	require.Len(t, hours, 1)
	require.EqualValues(t, 2048, hours[0].PromptTokens)
	require.EqualValues(t, 512, hours[0].OpportunityTokens)
	require.Equal(t, hours, counters.PreviousHour(now.Add(time.Hour)))
}

// The counter attachment must leave the production event payload byte-for-byte
// identical. Remove only slog's wall-clock envelope time.
func TestCacheTraceCountersDoNotChangeLogBytes(t *testing.T) {
	policies := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
	_, _, err := policies.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	var outputs []string
	previous := slog.Default()
	defer slog.SetDefault(previous)
	for _, attached := range []bool{false, true} {
		var logs bytes.Buffer
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}})))
		p := policies.Snapshot()
		s := &OpenAIGatewayService{}
		r := &OpenAIForwardResult{CacheDiagnostic: &OpenAICacheDiagnostic{GroupID: 7}, Usage: OpenAIUsage{InputTokens: 2048, CacheReadInputTokens: 1024},
			CacheTrace: &cacheTrace{policy: p, data: cacheTraceData{Transport: "http", UsagePromptPresent: true, UsagePromptObserved: true, UsageFields: 3, Prefix: cachePrefixObservation{Sampled: true, BytesOut: 4096, PreviousPrompt: 2048}}}}
		if attached {
			s.cacheCounters = NewCacheTraceCounters()
			r.cachePanel = &cachePanelRequest{policy: p}
			r.cachePanel.billed.Store(true)
		}
		s.settleCacheTrace(context.Background(), r, &Account{ID: 9, Platform: PlatformOpenAI})
		require.NotEmpty(t, logs.String())
		outputs = append(outputs, logs.String())
	}
	require.Equal(t, outputs[0], outputs[1])
}

func TestCacheTraceCountersUndeliveredUseFinalPayload(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, fault := range []string{"none", "observer_failed"} {
		t.Run(fault, func(t *testing.T) {
			s, c, result, body := t3SettlementForwardReply(t, "responses", false, traceForwardSSE, false)
			s.cacheCounters = NewCacheTraceCounters()
			if fault == "observer_failed" {
				live := cacheTraceFromGin(c)
				usage, complete := s.parseSSEUsageObserving(traceForwardSSE, func([]byte) error { return errors.New("synthetic observer failure") }, live)
				require.False(t, complete)
				result.Usage = *usage
				result.CacheTrace = nil
				s.finishCacheDiagnostic(c, body, result)
				require.NotNil(t, result.cacheTraceFailure)
			}
			p := CachePolicyFromGin(c)
			result.cachePanel = &cachePanelRequest{policy: p}
			resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
			t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
			ctx := disconnectedState(t, 0, 1)
			account := &Account{ID: 9, Platform: PlatformOpenAI, Type: "oauth"}
			logs := t3SettlementCapture(t)
			s.settleCacheTrace(ctx, result, account)
			s.recordCachePanelSettlement(result, &UsageLog{AccountID: 9}, account)
			s.settleCacheTrace(ctx, result, account)
			s.recordCachePanelTrace(result)
			response := s.cacheCounters.Snapshot(time.Now(), CachePanelQuery{Window: "1h", Platform: "all"}, p)
			coverage := response["coverage"].(map[string]any)
			require.EqualValues(t, 1, coverage["traced_requests"])
			require.EqualValues(t, result.Usage.InputTokens, coverage["traced_prompt_tokens"])
			require.InDelta(t, 1, *coverage["coverage_ratio"].(*float64), 1e-12)
			classes := response["loss_classes"].([]any)
			require.Len(t, classes, 1)
			item := classes[0].(map[string]any)
			require.Equal(t, "n/a_other", item["class"])
			require.Equal(t, true, item["excluded"])
			require.Nil(t, item["opportunity_tokens"])
			traces := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, traces, 1)
			payload := t3SettlementPayload(t, traces[0], true, p.Revision())
			reason := "undelivered"
			if fault == "observer_failed" {
				reason = fault
			}
			require.Equal(t, reason, payload["na_reason"])
		})
	}
}
