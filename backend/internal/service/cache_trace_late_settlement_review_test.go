package service

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestCacheTraceReviewPendingWorkerSettlesTimingAndPublishesLate(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
			s, c, req := rejectedTraceFixture(t, 32)
			s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
			live := cacheTraceFromGin(c)
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			telemetry := s.cacheTelemetry()
			if warm {
				telemetry.sessions[live.sessionKey] = openAICacheHistory{account: account.ID}
			}
			s.captureCacheTraceRequest(c, req, account, "responses")
			s.markCacheTraceSent(c)
			job := live.pending
			live.pending = nil // deterministic queued job; no worker goroutine
			defer job.cancelPending()
			state := live.async
			usage, ok := extractOpenAIUsageForCacheTrace(c, []byte(`{"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024},"output_tokens":1}}`))
			if !ok || !live.data.UsagePromptPresent || !live.data.UsagePromptObserved || state.result.Load() != nil {
				t.Fatal("pending worker incorrectly suppressed observed P presence")
			}
			result := &OpenAIForwardResult{RequestID: "review-pending", Usage: usage,
				CacheDiagnostic: &OpenAICacheDiagnostic{Session: live.sessionKey, GroupID: 10}}
			s.finishCacheTrace(c, result)
			s.observeCacheUsage(result, account, time.Now())
			var out bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
			defer slog.SetDefault(oldLogger)
			s.settleCacheTrace(context.Background(), result, account)
			settlement := state.settled.Load()
			if settlement == nil || settlement.owner == nil || settlement.prompt != 2048 {
				t.Fatal("not_ready request lost its delivered settlement/epoch")
			}
			if result.CacheTrace.data.LossClass != "n/a_sampled_out" || result.CacheTrace.data.NAReason != "not_ready" {
				t.Fatalf("pending worker changed event classification: %#v", result.CacheTrace.data)
			}
			telemetry.mu.Lock()
			next := cacheTraceSendLocked(telemetry, live.sessionKey, live.timing.sequence+1, settlement.at.Add(time.Second), cacheTraceWindow{})
			gap, _ := cacheTraceSettleTimingLocked(telemetry, live.sessionKey, next, settlement.at.Add(2*time.Second))
			telemetry.mu.Unlock()
			if gap.source != "settled" || gap.milliseconds != 1000 {
				t.Fatalf("delivered pending request was left in flight: %#v", gap)
			}
			frozenEvent := out.String()
			state.result.Store(&cacheTraceComputed{chain: cachePrefixChain{hashes: []uint64{7}, blocks: 1}})
			state.publish(s, live.sessionKey)
			if settlement.owner.chains[0].prompt != 2048 {
				t.Fatal("late worker could not publish delivered history")
			}
			s.settleCacheTrace(context.Background(), result, account)
			result.CacheTrace.emitCacheTrace()
			if out.String() != frozenEvent || bytes.Count(out.Bytes(), []byte(`"msg":"cache_trace"`)) != 1 ||
				result.CacheTrace.data.NAReason != "not_ready" || result.CacheTrace.data.Prefix.Sampled {
				t.Fatal("late result changed or duplicated frozen minimal event")
			}
		})
	}
}

func TestCacheTraceReviewExpiredWorkerClosesExistingTiming(t *testing.T) {
	s, c, req := rejectedTraceFixture(t, 32)
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	live := cacheTraceFromGin(c)
	telemetry := s.cacheTelemetry()
	telemetry.sessions[live.sessionKey] = openAICacheHistory{account: account.ID}
	s.captureCacheTraceRequest(c, req, account, "responses")
	s.markCacheTraceSent(c)
	live.async.reason.Store(cacheTraceExpired)
	result := &OpenAIForwardResult{RequestID: "review-expired", Usage: OpenAIUsage{InputTokens: 2048},
		CacheDiagnostic: &OpenAICacheDiagnostic{Session: live.sessionKey, GroupID: 10}}
	s.finishCacheTrace(c, result)
	s.observeCacheUsage(result, account, time.Now())
	s.settleCacheTrace(context.Background(), result, account)
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if telemetry.sessions[live.sessionKey].traceChains.recent[0].settledAt.IsZero() {
		t.Fatal("expired admitted request left an unsettled send record")
	}
}

func TestCacheTraceReviewBudgetRejectionDoesNotAllocateTimingStateAtSettlement(t *testing.T) {
	s, c, req := rejectedTraceFixture(t, 32)
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	s.cacheTraceWorkerPool().budget = 1
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	s.captureCacheTraceRequest(c, req, account, "responses")
	s.markCacheTraceSent(c)
	live := cacheTraceFromGin(c)
	result := &OpenAIForwardResult{RequestID: "review-budget", Usage: OpenAIUsage{InputTokens: 2048},
		CacheDiagnostic: &OpenAICacheDiagnostic{Session: live.sessionKey, GroupID: 10}}
	s.finishCacheTrace(c, result)
	s.observeCacheUsage(result, account, time.Now())
	s.settleCacheTrace(context.Background(), result, account)
	if s.cacheTelemetry().sessions[live.sessionKey].traceChains != nil {
		t.Fatal("never-admitted rejection allocated trace history state")
	}
}

func TestCacheTraceReviewObserverFailureClosesTimingWithoutPublishing(t *testing.T) {
	s, c, req := rejectedTraceFixture(t, 32)
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	live := cacheTraceFromGin(c)
	telemetry := s.cacheTelemetry()
	telemetry.sessions[live.sessionKey] = openAICacheHistory{account: account.ID}
	s.captureCacheTraceRequest(c, req, account, "responses")
	s.markCacheTraceSent(c)
	live.usageObservationFailed = true
	result := &OpenAIForwardResult{RequestID: "review-observer", Usage: OpenAIUsage{InputTokens: 2048},
		CacheDiagnostic: &OpenAICacheDiagnostic{Session: live.sessionKey, GroupID: 10}}
	s.finishCacheTrace(c, result)
	if result.CacheTrace != nil || result.cacheTraceFailure == nil {
		t.Fatal("observer failure did not preserve failure-only result")
	}
	s.observeCacheUsage(result, account, time.Now())
	s.settleCacheTrace(context.Background(), result, account)
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	owner := telemetry.sessions[live.sessionKey].traceChains
	if owner.recent[0].settledAt.IsZero() || owner.chains[0].blocks != 0 {
		t.Fatal("observer failure failed to close timing or published a failed chain")
	}
}
