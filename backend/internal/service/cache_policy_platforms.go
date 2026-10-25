package service

import "encoding/json"

const (
	cacheTracePlatformOpenAI uint8 = 1 << iota
	cacheTracePlatformAnthropic
	cacheTracePlatformGemini
	cacheTracePlatformMessagesBridge
)

// WithProviderCacheTrace enables the M2b build capability, never the runtime
// switch. Keeping it explicit preserves frozen M1/M2 contract replays.
func WithProviderCacheTrace() CachePolicyOption {
	return func(s *CachePolicyService) { s.providerTraceAvailable = true }
}

func cacheTracePlatformBit(platform string) uint8 {
	switch platform {
	case "openai":
		return cacheTracePlatformOpenAI
	case "anthropic":
		return cacheTracePlatformAnthropic
	case "gemini":
		return cacheTracePlatformGemini
	case "messages_bridge":
		return cacheTracePlatformMessagesBridge
	default:
		return 0
	}
}

func cacheTracePlatformsMask(value any) uint8 {
	var mask uint8
	switch platforms := value.(type) {
	case []string:
		for _, platform := range platforms {
			mask |= cacheTracePlatformBit(platform)
		}
	case []any:
		for _, platform := range platforms {
			if name, ok := platform.(string); ok {
				mask |= cacheTracePlatformBit(name)
			}
		}
	}
	return mask
}

// TraceEnabledFor reads only the immutable request snapshot.
func (p *CachePolicySnapshot) TraceEnabledFor(platform string) bool {
	if !p.TraceEnabled() {
		return false
	}
	mask := p.platforms
	if mask == 0 {
		// Preserve old explicitly constructed OpenAI-only snapshots.
		mask = cacheTracePlatformOpenAI
	}
	return mask&cacheTracePlatformBit(platform) != 0
}

func (p *CachePolicySnapshot) IngressChainPercent() int {
	if p == nil {
		return 0
	}
	return p.ingressChainPercent
}

func parseCacheTracePlatforms(raw json.RawMessage) ([]string, string) {
	var platforms []string
	if string(raw) == "null" || json.Unmarshal(raw, &platforms) != nil {
		return nil, "malformed_body"
	}
	if len(platforms) == 0 {
		return nil, "empty"
	}
	var seen uint8
	for _, platform := range platforms {
		bit := cacheTracePlatformBit(platform)
		if bit == 0 {
			return nil, "invalid_option"
		}
		if seen&bit != 0 {
			return nil, "duplicate"
		}
		seen |= bit
	}
	return platforms, ""
}

func validateCacheIngressPercent(out, before map[string]any, patch map[string]json.RawMessage, diff []CachePolicyDiff, errs []CachePolicyFieldError, writing bool) ([]CachePolicyDiff, []CachePolicyFieldError) {
	if _, exists := patch["ingress_chain_percent"]; !writing && !exists {
		// Upgrade old envelopes in memory only. This normalized entry is also
		// the before value for validate/PUT; no setting write or rev bump.
		out["ingress_chain_percent"] = min(10, policyInt(out["chain_percent"]))
	}
	for _, err := range errs {
		if err.Field == "ingress_chain_percent" || err.Field == "chain_percent" {
			return diff, errs
		}
	}
	if policyInt(out["ingress_chain_percent"]) <= policyInt(out["chain_percent"]) {
		return diff, errs
	}
	errs = append(errs, CachePolicyFieldError{Field: "ingress_chain_percent", Code: "exceeds_chain_percent", Limit: map[string]int{"max": policyInt(out["chain_percent"])}})
	out["ingress_chain_percent"] = before["ingress_chain_percent"]
	valid := diff[:0]
	for _, change := range diff {
		if change.Field != "ingress_chain_percent" {
			valid = append(valid, change)
		}
	}
	return valid, errs
}
