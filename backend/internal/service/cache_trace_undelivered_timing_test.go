//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCacheTraceUndeliveredClosesSendWithoutEventOrHistory(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "observed", true: "observer_failed"}[failed], func(t *testing.T) {
			s := &OpenAIGatewayService{cfg: &config.Config{}}
			telemetry := s.cacheTelemetry()
			telemetry.sessions["session"] = openAICacheHistory{account: 9}
			telemetry.mu.Lock()
			timing := cacheTraceSendLocked(telemetry, "session", 1, time.Now().Add(-time.Second), cacheTraceWindow{})
			telemetry.mu.Unlock()
			pending := &cacheTraceAsync{}
			trace := &cacheTrace{timing: timing, async: pending}
			result := &OpenAIForwardResult{CacheTrace: trace, CacheDiagnostic: &OpenAICacheDiagnostic{Session: "session"}}
			if failed {
				result.CacheTrace = nil
				result.cacheTraceFailure = &cacheTraceFailure{timing: timing}
			}
			logs := t3SettlementCapture(t)
			ctx := disconnectedState(t, 0, 1)
			s.settleCacheTrace(ctx, result, &Account{ID: 9, Platform: PlatformOpenAI})
			s.settleCacheTrace(ctx, result, &Account{ID: 9, Platform: PlatformOpenAI})
			require.Empty(t, t3SettlementEvents(t, logs, "cache_trace"))
			require.Nil(t, pending.settled.Load(), "an undelivered request cannot train history")
			if !failed {
				require.True(t, trace.settled)
				require.True(t, trace.emitted, "a later settlement cannot emit the suppressed event")
				require.Nil(t, trace.async)
			}
			telemetry.mu.Lock()
			next := cacheTraceSendLocked(telemetry, "session", 2, time.Now(), cacheTraceWindow{})
			gap, _ := cacheTraceSettleTimingLocked(telemetry, "session", next, time.Now())
			telemetry.mu.Unlock()
			require.Equal(t, "settled", gap.source, "the previous undelivered send is finished")
		})
	}
}

func TestCacheTraceUndeliveredRespectsSessionCap(t *testing.T) {
	for _, abuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_billed", true: "abuse"}[abuse], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.CacheTrace.MaxSessions = 1
			s := &OpenAIGatewayService{cfg: cfg}
			telemetry := s.cacheTelemetry()
			telemetry.sessions["old"] = openAICacheHistory{at: time.Now().Add(-time.Hour), traceChains: &cacheTraceSessionState{}}
			telemetry.sessions["current"] = openAICacheHistory{at: time.Now()}
			result := &OpenAIForwardResult{
				CacheDiagnostic: &OpenAICacheDiagnostic{Session: "current"},
				CacheTrace:      &cacheTrace{timing: cacheTraceTimingToken{sequence: 1, sentAt: time.Now().Add(-time.Second)}},
			}
			logs := t3SettlementCapture(t)
			s.settleCacheTraceUndelivered(result, &Account{ID: 9, Platform: PlatformOpenAI}, abuse)
			require.Nil(t, telemetry.sessions["old"].traceChains)
			require.NotNil(t, telemetry.sessions["current"].traceChains)
			if abuse {
				require.Len(t, t3SettlementEvents(t, logs, "cache_trace"), 1)
			} else {
				require.Empty(t, t3SettlementEvents(t, logs, "cache_trace"))
			}
		})
	}
}
