package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Called only when the forward result carries an enabled observation. The
// legacy usage update runs first; neither billing nor routing state is changed.
func (s *OpenAIGatewayService) settleCacheTrace(ctx context.Context, result *OpenAIForwardResult, account *Account) {
	if result == nil || (result.CacheTrace == nil && result.cacheTraceFailure == nil) {
		return
	}
	// Event/observer failures must never escape into billing. No error or panic
	// value is logged, and no recover is added to a frame traversal.
	defer func() { _ = recover() }()
	undeliveredAbuse := false
	if st := resilience.StateFrom(ctx); st != nil {
		disconnected, delivered, abuse := st.Disconnect()
		if disconnected && !delivered {
			if !abuse {
				return
			}
			undeliveredAbuse = true
		}
	}
	diagnostic := result.CacheDiagnostic
	if diagnostic == nil || account == nil || account.Platform != PlatformOpenAI {
		return
	}
	if undeliveredAbuse {
		s.settleCacheTraceUndelivered(result, account)
		return
	}
	if failed := result.cacheTraceFailure; failed != nil {
		if !failed.emitted.CompareAndSwap(false, true) {
			return
		}
		// Failed observation drops its chain but still closes an admitted send.
		if failed.timing.sequence != 0 && !failed.timing.sentAt.IsZero() {
			telemetry := s.cacheTelemetry()
			telemetry.mu.Lock()
			cacheTraceSettleTimingLocked(telemetry, diagnostic.Session, failed.timing, time.Now())
			telemetry.mu.Unlock()
		}
		d := failed.data
		d.SessionDigest, d.GroupID, d.AccountID = diagnostic.Session, diagnostic.GroupID, account.ID
		d.PromptTokens, d.ReadTokens = result.Usage.InputTokens, result.Usage.CacheReadInputTokens
		emitCacheTraceRejected(d)
		return
	}
	t := result.CacheTrace
	t.mu.Lock()
	if t.settled {
		t.mu.Unlock()
		return
	}
	t.settled = true
	t.applyCacheTraceComputed()
	async := t.async
	t.async = nil // exactly one worker snapshot; a late worker cannot alter this event
	d := &t.data
	d.SessionDigest, d.GroupID, d.AccountID = diagnostic.Session, diagnostic.GroupID, account.ID
	if diagnostic.Session == "" {
		d.GapSource, d.GapSendMS, d.WindowSequenceDelta = "no_session", 0, -1
	}
	d.PromptTokens, d.ReadTokens = result.Usage.InputTokens, result.Usage.CacheReadInputTokens
	now := time.Now()
	if !d.Prefix.historyAt.IsZero() {
		d.Prefix.HistoryAgeSeconds = max(0, int(now.Sub(d.Prefix.historyAt).Seconds()))
	}
	minimum := cacheTraceDefaultMinCacheableTokens
	if s.cfg != nil && s.cfg.Gateway.CacheTrace.MinCacheableTokens > 0 {
		minimum = s.cfg.Gateway.CacheTrace.MinCacheableTokens
	}
	o := d.Prefix
	classified := classifyCacheTrace(cacheTraceClassificationInput{
		Transport: d.Transport, UsageObserved: d.UsagePromptObserved, UsagePromptPresent: d.UsagePromptPresent, UsageFields: d.UsageFields,
		PromptTokens: d.PromptTokens, ReadTokens: d.ReadTokens, CoverageReason: d.NAReason,
		ChainSampled: o.Sampled, HistoryUnsampled: o.HistoryUnsampled, BytesOut: o.BytesOut,
		ForkRef: o.ForkRef, ForkKind: o.ForkKind, ForkPrecision: o.ForkPrecision, ForkByte: o.ForkByte, ForkByteHi: o.ForkByteHi, PreviousPrompt: o.PreviousPrompt,
		TailKind: o.TailKind, ModelGen: o.ModelGen, ReasoningContext: o.ReasoningContext, ReasoningContextEffective: d.ReasoningContextEffective,
		OldReasoningN: o.OldReasoningN, FirstOldReasoningByte: o.FirstOldReasoningByte,
	}, minimum)
	d.LossClass, d.NAReason = classified.LossClass, classified.NAReason
	d.TokensPerByteMilli, d.ReasoningBoundaryHit, d.ReadShortOfFork = classified.TokensPerByteMilli, classified.ReasoningBoundaryHit, classified.ReadShortOfFork
	var owner *cacheTraceSessionState
	// A minimal event can still represent an admitted request whose worker
	// is pending/expired. Close its send record, and allow a pending worker
	// to publish history later without changing this frozen event.
	if t.timing.sequence != 0 && !t.timing.sentAt.IsZero() {
		telemetry := s.cacheTelemetry()
		telemetry.mu.Lock()
		gap, state := cacheTraceSettleTimingLocked(telemetry, diagnostic.Session, t.timing, now)
		telemetry.mu.Unlock()
		d.GapSource, d.GapSendMS, owner = gap.source, gap.milliseconds, state
	}
	prompt, present, session := d.PromptTokens, d.UsagePromptPresent, diagnostic.Session
	t.mu.Unlock()
	// Missing P can never train a subsequent comparison. Delayed publication is
	// bound to the observed session epoch, not just its reused digest.
	if present && async != nil && owner != nil {
		async.settled.Store(&cacheTraceSettlement{prompt: prompt, at: now, owner: owner})
		async.publish(s, session)
	}
	t.emitCacheTrace()
}

// Already-attempted requests billed under the existing abuse policy retain one
// minimal joinable event. No body/usage reparse or loss attribution is performed.
// The caller provides validated observation, diagnostic and account pointers and
// isolates all observer failures from billing with its request-level recover.
func (s *OpenAIGatewayService) settleCacheTraceUndelivered(result *OpenAIForwardResult, account *Account) {
	var d cacheTraceData
	var timing cacheTraceTimingToken
	if failed := result.cacheTraceFailure; failed != nil {
		if !failed.emitted.CompareAndSwap(false, true) {
			return
		}
		d, timing = failed.data, failed.timing
	} else {
		t := result.CacheTrace
		t.mu.Lock()
		if t.settled || t.emitted {
			t.mu.Unlock()
			return
		}
		t.settled, t.emitted = true, true
		d, timing = t.data, t.timing
		t.async = nil // never publish undelivered history, including late results
		t.mu.Unlock()
	}
	diagnostic := result.CacheDiagnostic
	d.SessionDigest, d.GroupID, d.AccountID = diagnostic.Session, diagnostic.GroupID, account.ID
	d.PromptTokens, d.ReadTokens = result.Usage.InputTokens, result.Usage.CacheReadInputTokens
	d.NAReason = "undelivered"
	if timing.sequence != 0 && !timing.sentAt.IsZero() {
		telemetry := s.cacheTelemetry()
		telemetry.mu.Lock()
		cacheTraceSettleTimingLocked(telemetry, diagnostic.Session, timing, time.Now())
		telemetry.mu.Unlock()
	}
	emitCacheTraceRejected(d)
}
