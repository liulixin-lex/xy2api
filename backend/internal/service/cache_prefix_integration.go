package service

import (
	"github.com/tidwall/gjson"
	"strings"
	"time"
)

// captureCachePrefix is read-only with respect to telemetry. Only settlement
// may publish a chain, so a failed/undelivered attempt cannot become history.
func (s *OpenAIGatewayService) captureCachePrefix(t *cacheTrace, body []byte, protocol string, cutoff ...time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	session, anchor, transport := t.sessionKey, t.data.ConvAnchor, t.data.Transport
	t.mu.Unlock()
	observation := cachePrefixUnavailable(len(body))
	var chain cachePrefixChain
	if transport != "plugin" && sha256SumCacheCohort(session) < s.cfg.Gateway.CacheTrace.ChainPercent {

		telemetry := s.cacheTelemetry()
		telemetry.mu.Lock()
		previous, exists := telemetry.sessions[session]
		var histories [cachePrefixHistoryCount]cachePrefixChain
		if previous.traceChains != nil {
			histories = *previous.traceChains
			if len(cutoff) > 0 {
				eligible := [cachePrefixHistoryCount]cachePrefixChain{}
				n := 0
				for _, old := range histories {
					if old.blocks > 0 && !old.settledAt.After(cutoff[0]) {
						eligible[n] = old
						n++
					}
				}
				histories = eligible
			}
		}
		telemetry.mu.Unlock()
		var offsets cachePrefixPositions
		chain, offsets, observation = scanCachePrefix(body, protocol, cachePrefixProcessSeed, s.cfg.Gateway.CacheTrace.ReasoningContextPaths, histories)
		chain.anchor = anchor
		hasHistory := false
		for _, old := range histories {
			hasHistory = hasHistory || old.blocks > 0
		}
		observation.HistoryUnsampled = exists && !hasHistory
		compareCachePrefix(chain, offsets, histories, &observation)
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
	if chain.settledAt.IsZero() {
		chain.settledAt = time.Now()
	}
	telemetry := s.cacheTelemetry()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	previous, exists := telemetry.sessions[session]
	if !exists {
		return
	}
	if previous.traceChains == nil {
		previous.traceChains = new([cachePrefixHistoryCount]cachePrefixChain)
	}

	for i := 0; i < cachePrefixHistoryCount; i++ {
		if previous.traceChains[i].settledAt.After(chain.settledAt) {
			continue
		}
		for j := cachePrefixHistoryCount - 1; j > i; j-- {
			previous.traceChains[j] = previous.traceChains[j-1]
		}
		previous.traceChains[i] = chain
		break
	}
	telemetry.sessions[session] = previous
}

func cachePrefixUnavailable(rawBytes int) cachePrefixObservation {
	return cachePrefixObservation{BytesOutRaw: rawBytes, BytesOut: -1, BlockN: -1, ForkBlock: -1, ForkByte: -1, ForkRawByte: -1,
		ForkRef: "none", ForkPrecision: "none", ForkKind: "none", ForkBlockHi: -1, ForkByteHi: -1, PreviousPrompt: -1, HistoryAgeSeconds: -1,
		TailKind: "n/a", ReasoningContext: "n/a", ModelGen: "n/a", ReasoningItemsN: -1, OldReasoningN: -1, ReasoningEncryptedN: -1,
		FirstOldReasoningBlock: -1, FirstOldReasoningByte: -1, FirstOldReasoningRawByte: -1}
}

// Worker-only. All body scans, including absence checks, are off the request
// goroutine. Category fields are still computed when chain sampling is off.
func (s *OpenAIGatewayService) computeCacheTrace(ingress, body []byte, apiKeyID int64, headerSource bool, session, protocol, transport string, cutoff ...time.Time) *cacheTraceComputed {

	var pck, instructions, items, messages gjson.Result
	var thread bool
	parseRawJSONView(ingress).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "prompt_cache_key":
			pck = v
		case "instructions":
			instructions = v
		case "input":
			items = v
		case "messages":
			messages = v
		case "client_metadata":
			thread = v.Get("thread_id").Exists()
		}
		return true
	})
	if !items.Exists() {
		items = messages
	}
	r := &cacheTraceComputed{anchor: s.cacheTraceAnchorParts(apiKeyID, instructions, items), pckIn: cacheTracePCKIn(pck), idSource: "content_seed", thread: thread}
	if headerSource {
		r.idSource = "header"
	} else if strings.TrimSpace(pck.String()) != "" {
		r.idSource = "body_pck"
	}

	trace := &cacheTrace{sessionKey: session, data: cacheTraceData{ConvAnchor: r.anchor, Transport: transport}}
	s.captureCachePrefix(trace, body, protocol, cutoff...)
	r.prefix, r.chain = trace.data.Prefix, trace.chain
	if r.prefix.Sampled {
		r.pckExists, r.pckUUID = r.prefix.pckExists, r.prefix.pckUUID
	} else {
		out := parseRawJSONView(body).Get("prompt_cache_key")
		r.pckExists = out.Exists()
		r.pckUUID = out.Type == gjson.String && cacheTraceUUID.MatchString(out.Str)
	}
	return r
}
