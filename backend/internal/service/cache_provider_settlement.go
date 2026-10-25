package service

import (
	"math"
	"time"
)

// cacheProviderBusiness carries only bounded aggregation dimensions from the
// already selected provider request. The session value is the scoped HMAC
// digest, never the local routing hash.
type cacheProviderBusiness struct {
	platform string
	host     string
	session  string
}

func cacheProviderTraceCounterSample(data cacheTraceData) CacheTraceCounterSample {
	return CacheTraceCounterSample{
		PromptTokens:    data.PromptTokens,
		ReadTokens:      data.ReadTokens,
		PreviousPrompt:  data.Prefix.PreviousPrompt,
		LossClass:       data.LossClass,
		Transport:       data.Transport,
		ModelGen:        data.Prefix.ModelGen,
		ForkKind:        data.Prefix.ForkKind,
		PCKClassIn:      "unknown",
		PCKClassOut:     "unknown",
		SessOutRelation: "unknown",
		RouteReason:     data.RouteReason,
		RebindCause:     data.RebindCause,
	}
}

func (s *GatewayService) recordProviderCacheBusiness(info *cacheProviderBusiness, usage ClaudeUsage, result *ForwardResult, account *Account) {
	if s == nil || s.providerTraceEngine == nil || s.providerTraceEngine.cacheCounters == nil ||
		info == nil || info.platform == "" || account == nil || result == nil {
		return
	}
	prompt := int64(max(0, usage.InputTokens+usage.CacheReadInputTokens+usage.CacheCreationInputTokens))
	if prompt == 0 {
		return
	}
	model := result.Model
	s.providerTraceEngine.cacheCounters.RecordPlatformBusiness(
		time.Now().UTC(), info.platform, prompt, int64(max(0, usage.CacheReadInputTokens)),
		info.host, info.session, model, account.ID,
	)
}

func (s *OpenAIGatewayService) settleProviderCacheTrace(result *ForwardResult, delivery cacheTraceDelivery, emit bool) {
	if result == nil || result.providerTrace == nil {
		return
	}
	defer func() { _ = recover() }()
	t := result.providerTrace
	t.mu.Lock()
	if t.settled {
		t.mu.Unlock()
		return
	}
	t.settled = true
	t.applyCacheTraceComputed()
	async := t.async
	t.async = nil
	now := time.Now()
	gap, owner := s.providerHistory.settle(t.providerKey, t.timing, now)
	d := &t.data
	d.GapSource, d.GapSendMS = gap.source, gap.milliseconds
	// Billing's finalized mutually-exclusive buckets are authoritative for P/read.
	d.PromptTokens = max(0, result.Usage.InputTokens+result.Usage.CacheReadInputTokens+result.Usage.CacheCreationInputTokens)
	d.ReadTokens = max(0, result.Usage.CacheReadInputTokens)
	if !d.Prefix.historyAt.IsZero() {
		d.Prefix.HistoryAgeSeconds = max(0, int(now.Sub(d.Prefix.historyAt).Seconds()))
	}
	if !emit || delivery == cacheTraceUnbilled {
		t.emitted = true
		t.mu.Unlock()
		return
	}
	if t.usageObservationFailed {
		d.LossClass = "n/a_sampled_out"
		d.NAReason = "observer_failed"
	} else if delivery == cacheTraceAbuse {
		d.LossClass = "n/a_sampled_out"
		d.NAReason = "undelivered"
	} else {
		bits := uint8(0)
		if d.UsageFields&9 != 0 {
			bits = 1
		}
		o := d.Prefix
		minimum := cacheTraceDefaultMinCacheableTokens
		if s.cfg != nil && s.cfg.Gateway.CacheTrace.MinCacheableTokens > 0 {
			minimum = s.cfg.Gateway.CacheTrace.MinCacheableTokens
		}
		cl := classifyCacheTrace(cacheTraceClassificationInput{
			Transport: d.Transport, UsageObserved: d.UsagePromptObserved, UsagePromptPresent: d.UsagePromptPresent, UsageFields: bits,
			PromptTokens: d.PromptTokens, ReadTokens: d.ReadTokens, CoverageReason: d.NAReason,
			ChainSampled: o.Sampled, HistoryUnsampled: o.HistoryUnsampled, BytesOut: o.BytesOut,
			ForkRef: o.ForkRef, ForkKind: o.ForkKind, ForkPrecision: o.ForkPrecision, ForkByte: o.ForkByte, ForkByteHi: o.ForkByteHi,
			PreviousPrompt: o.PreviousPrompt, TailKind: o.TailKind,
		}, minimum)
		d.LossClass, d.NAReason = cl.LossClass, cl.NAReason
		d.TokensPerByteMilli, d.ReadShortOfFork = cl.TokensPerByteMilli, cl.ReadShortOfFork
		if d.Platform == PlatformAnthropic && d.LossClass == "under_read" && d.Provider.lastTurnOnly && d.Prefix.TailKind == "user_msg" && d.Provider.ThinkingBlocks > 0 && d.Provider.FirstThinkingByte >= 0 && d.Prefix.BytesOut > 0 {
			estimated := float64(d.Provider.FirstThinkingByte) * float64(d.PromptTokens) / float64(d.Prefix.BytesOut)
			if math.Abs(float64(d.ReadTokens)-estimated) <= cacheTraceEstimatedTolerance(estimated) {
				d.LossClass = "provider_thinking_strip"
			}
		}
	}
	publish := delivery == cacheTraceDelivered && !t.usageObservationFailed && d.UsagePromptPresent && async != nil && owner != nil
	data := *d
	t.mu.Unlock()
	if publish {
		async.settled.Store(&cacheTraceSettlement{prompt: data.PromptTokens, at: now, owner: owner})
		async.publish(s, t.sessionKey)
	}
	t.emitCacheTrace()
	if s.cacheCounters != nil {
		s.cacheCounters.RecordPlatformTrace(now, data.Platform, cacheProviderTraceCounterSample(data))
	}
}
