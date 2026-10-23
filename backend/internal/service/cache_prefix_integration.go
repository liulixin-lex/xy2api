package service

// captureCachePrefix is read-only with respect to telemetry. Only settlement
// may publish a chain, so a failed/undelivered attempt cannot become history.
func (s *OpenAIGatewayService) captureCachePrefix(t *cacheTrace, body []byte, protocol string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	session, anchor, transport := t.sessionKey, t.data.ConvAnchor, t.data.Transport
	t.mu.Unlock()
	observation := cachePrefixObservation{BytesOutRaw: len(body), ForkRawByte: -1, FirstOldReasoningRawByte: -1, ForkBlock: -1, ForkByte: -1, ForkRef: "none", TailKind: "other", ReasoningContext: "absent", ModelGen: "other_openai", FirstOldReasoningBlock: -1, FirstOldReasoningByte: -1}
	var chain cachePrefixChain
	if transport != "plugin" && sha256SumCacheCohort(session) < s.cfg.Gateway.CacheTrace.ChainPercent {
		var offsets cachePrefixPositions
		chain, offsets, observation = scanCachePrefix(body, protocol, cachePrefixProcessSeed, s.cfg.Gateway.CacheTrace.ReasoningContextPaths)
		chain.anchor = anchor
		telemetry := s.cacheTelemetry()
		telemetry.mu.Lock()
		previous, exists := telemetry.sessions[session]
		telemetry.mu.Unlock()
		observation.HistoryUnsampled = exists && len(previous.traceChains[0].hashes) == 0
		compareCachePrefix(chain, offsets, previous.traceChains, &observation)
	}
	t.mu.Lock()
	t.chain = chain
	t.data.Prefix = observation
	t.mu.Unlock()
}

// observeCacheUsage creates/evicts the existing session entry first. Do not
// create a second map, a second lock, or resurrect an entry evicted meanwhile.
func (s *OpenAIGatewayService) rememberCachePrefix(session string, chain cachePrefixChain, prompt int) {
	if len(chain.hashes) == 0 {
		return
	}
	chain.prompt = prompt
	telemetry := s.cacheTelemetry()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	previous, exists := telemetry.sessions[session]
	if !exists {
		return
	}
	for i := cachePrefixHistoryCount - 1; i > 0; i-- {
		previous.traceChains[i] = previous.traceChains[i-1]
	}
	previous.traceChains[0] = chain
	telemetry.sessions[session] = previous
}
