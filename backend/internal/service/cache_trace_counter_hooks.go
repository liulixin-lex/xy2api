package service

import (
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Allocated only while observation is enabled. Both the billing and event
// callbacks can arrive first; each counter is committed once after both facts.
type cachePanelRequest struct {
	policy   *CachePolicySnapshot
	business atomic.Bool
	billed   atomic.Bool
	traced   atomic.Bool
	rejected atomic.Pointer[cacheTraceData]
}

func (s *OpenAIGatewayService) recordCachePanelSettlement(result *OpenAIForwardResult, usage *UsageLog, account *Account) {
	defer func() { _ = recover() }()
	state := result.cachePanel
	if state == nil || !state.policy.TraceEnabled() || s.cacheCounters == nil || account == nil || account.Platform != PlatformOpenAI || result.Usage.InputTokens <= 0 {
		return
	}
	row := CacheNorthstarRow{HasMedia: usage.ImageCount > 0 || usage.VideoCount > 0, CacheNorthstarTotals: CacheNorthstarTotals{PromptTokens: int64(result.Usage.InputTokens)}}
	if usage.GroupID != nil {
		row.GroupID = *usage.GroupID
	}
	if !state.policy.northstarSpec.Business(row) {
		return
	}
	if !state.business.CompareAndSwap(false, true) {
		return
	}
	host := "unknown"
	if account.Type == "oauth" {
		host = "oauth:openai"
	} else if parsed, err := url.Parse(account.GetOpenAIBaseURL()); err == nil && parsed.Hostname() != "" {
		host = strings.ToLower(parsed.Hostname())
	}
	session, model := "", result.Model
	if result.CacheDiagnostic != nil {
		session = result.CacheDiagnostic.Session
		model = result.CacheDiagnostic.Model
	}
	// Raw P/read matches trace and avoids mistaking local billing rewriting for
	// an upstream cache hit. The northstar rollup separately uses billed buckets.
	s.cacheCounters.RecordBusiness(time.Now(), int64(result.Usage.InputTokens), int64(result.Usage.CacheReadInputTokens), host, session, model, usage.AccountID)
	state.billed.Store(true)
	s.recordCachePanelTrace(result)
}

func (s *OpenAIGatewayService) recordCachePanelTrace(result *OpenAIForwardResult) {
	defer func() { _ = recover() }()
	if result == nil || result.cachePanel == nil || s.cacheCounters == nil {
		return
	}
	state := result.cachePanel
	if !state.policy.TraceEnabled() || !state.billed.Load() {
		return
	}
	var data cacheTraceData
	if rejected := state.rejected.Load(); rejected != nil {
		data = *rejected
	} else if trace := result.CacheTrace; trace != nil {
		trace.mu.Lock()
		emitted := trace.emitted
		data = trace.data
		trace.mu.Unlock()
		if !emitted {
			return
		}
	} else if failed := result.cacheTraceFailure; failed != nil {
		if !failed.emitted.Load() {
			return
		}
		data = failed.data
		data.LossClass = "n/a_sampled_out"
		data.PromptTokens, data.ReadTokens = result.Usage.InputTokens, result.Usage.CacheReadInputTokens
	} else {
		return
	}
	if !state.traced.CompareAndSwap(false, true) {
		return
	}
	s.cacheCounters.RecordTrace(time.Now(), data)
}
