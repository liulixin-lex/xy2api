//go:build unit

package service

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Exercise actual Forward results and RecordUsage with the existing delivery
// state at settlement. A disconnected abuse request is still billed upstream
// cost, but its cache event is a minimal coverage record, never a loss estimate.
func TestCacheTraceUndeliveredAbuseMinimalEventAndBilling(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, protocol := range []string{"responses", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "undelivered-"+protocol, protocol)
			logs := t3SettlementCapture(t)
			result := f.forward(t, protocol, false)
			upstream, ok := f.s.httpUpstream.(*httpUpstreamRecorder)
			require.True(t, ok)
			require.Len(t, upstream.requests, 1, "a real upstream attempt preceded settlement")
			awaitCacheTrace(t, f.s, f.c, result)
			t3SettlementWorkerDrained(t, f.s)
			pending := result.CacheTrace.async
			require.NotNil(t, pending)
			resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
			t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
			ctx := disconnectedState(t, 0, f.api.ID)
			disconnected, delivered, abuse := resilience.StateFrom(ctx).Disconnect()
			require.True(t, disconnected)
			require.False(t, delivered)
			require.True(t, abuse)
			require.Empty(t, t3SettlementEvents(t, logs, "cache_trace"), "attempted but not settled emits no event")
			require.Empty(t, t3SettlementEvents(t, logs, "openai_cache_usage"))
			require.Zero(t, f.rows.calls)
			input := &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account,
				PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
			require.NoError(t, f.s.RecordUsage(ctx, input))
			require.Equal(t, 1, f.rows.calls)
			require.Equal(t, 1, f.billing.calls)
			require.NotNil(t, f.rows.lastLog.BillingMode)
			require.Equal(t, UsageBillingModeUndeliveredAbuse, *f.rows.lastLog.BillingMode)
			_, _, billed := resilience.StateFrom(ctx).UndeliveredUsage()
			require.True(t, billed)
			f.s.settleCacheTrace(ctx, result, f.account)
			result.CacheTrace.emitCacheTrace()
			usage := t3SettlementEvents(t, logs, "openai_cache_usage")
			traces := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, usage, 1)
			require.Len(t, traces, 1, "repeated observer settlement cannot duplicate the event")
			payload := t3SettlementPayload(t, traces[0], true, CachePolicyFromGin(f.c).Revision())
			require.Equal(t, "n/a_sampled_out", payload["loss_class"])
			require.Equal(t, "undelivered", payload["na_reason"])
			t3SettlementAssertUsageLink(t, traces[0], usage[0], result, f.account)
			require.Nil(t, pending.settled.Load(), "abnormal delivery never publishes a cache history")
			t3SettlementAssertPrivate(t, logs)
		})
	}
}

// A request may reach the billing entry without ever calling Forward (including
// an abuse charge). A scheduled ingress trace alone is not an attempted result.
func TestCacheTraceZeroUpstreamAttemptsEmitNeitherEvent(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, abuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "abuse_charge"}[abuse], func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "zero-attempt", "responses")
			require.NotEmpty(t, f.s.GenerateSessionHash(f.c, f.body))
			require.NotNil(t, cacheTraceFromGin(f.c), "ingress exists but no upstream attempt has occurred")
			upstream := &httpUpstreamRecorder{resp: traceForwardOK()}
			f.s.httpUpstream = upstream
			result := &OpenAIForwardResult{RequestID: f.id, Model: "gpt-5.4", UpstreamModel: "gpt-5.4",
				Usage: OpenAIUsage{InputTokens: 12000, CacheReadInputTokens: 4096}, Duration: time.Second}
			ctx := context.Background()
			if abuse {
				resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
				t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
				ctx = disconnectedState(t, 0, f.api.ID)
			}
			logs := t3SettlementCapture(t)
			require.NoError(t, f.s.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account,
				PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}))
			require.Equal(t, 1, f.rows.calls, "the billing entry was actually exercised")
			require.Equal(t, 1, f.billing.calls)
			require.Empty(t, upstream.requests)
			require.Nil(t, result.CacheDiagnostic)
			require.Nil(t, result.CacheTrace)
			require.Nil(t, result.cacheTraceFailure)
			require.Empty(t, t3SettlementEvents(t, logs, "cache_trace"))
			require.Empty(t, t3SettlementEvents(t, logs, "openai_cache_usage"))
			t3SettlementAssertPrivate(t, logs)
		})
	}
}

// The event emitter is untrusted with respect to billing, including the new
// undelivered path. A failed logger must not escape or cause a billing retry.
type cacheTraceUndeliveredFailingHandler struct {
	base  slog.Handler
	fault string
	calls *atomic.Int64
}

func (h cacheTraceUndeliveredFailingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}
func (h cacheTraceUndeliveredFailingHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "cache_trace" {
		h.calls.Add(1)
		if h.fault == "panic" {
			panic("synthetic-undelivered-observer-fault")
		}
		return errors.New("synthetic-undelivered-observer-fault")
	}
	return h.base.Handle(ctx, record)
}
func (h cacheTraceUndeliveredFailingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.base = h.base.WithAttrs(attrs)
	return h
}
func (h cacheTraceUndeliveredFailingHandler) WithGroup(name string) slog.Handler {
	h.base = h.base.WithGroup(name)
	return h
}

func TestCacheTraceUndeliveredLogFailureCannotChangeBilling(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, fault := range []string{"panic", "error"} {
		t.Run(fault, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "undelivered-log-"+fault, "responses")
			result := f.forward(t, "responses", false)
			awaitCacheTrace(t, f.s, f.c, result)
			t3SettlementWorkerDrained(t, f.s)
			resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
			t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
			ctx := disconnectedState(t, 0, f.api.ID)
			logs := t3SettlementCapture(t)
			previous := slog.Default()
			var calls atomic.Int64
			slog.SetDefault(slog.New(cacheTraceUndeliveredFailingHandler{base: previous.Handler(), fault: fault, calls: &calls}))
			t.Cleanup(func() { slog.SetDefault(previous) })
			require.NotPanics(t, func() {
				require.NoError(t, f.s.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account,
					PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}))
			})
			f.s.settleCacheTrace(ctx, result, f.account)
			require.Equal(t, int64(1), calls.Load())
			require.Equal(t, 1, f.rows.calls)
			require.Equal(t, 1, f.billing.calls)
			require.Equal(t, UsageBillingModeUndeliveredAbuse, *f.rows.lastLog.BillingMode)
			require.Len(t, t3SettlementEvents(t, logs, "openai_cache_usage"), 1)
			require.NotContains(t, logs.String(), "synthetic-undelivered-observer-fault")
		})
	}
}
