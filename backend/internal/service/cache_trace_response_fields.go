package service

import (
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// Consume already indexed small response fields only. All
// returned strings are fixed literals, never borrowed upstream string views.
type cacheTraceResponseObservation struct {
	usageFields                 uint8
	promptPresent               bool
	usagePresent                bool
	typedPrompt                 [2]bool
	typedUsageFields            [2]uint8
	metadataPresent             uint8
	reasoning, diagnostics, ttl string
}

var cacheTraceDiagnosticNames = [...]string{
	"model_changed", "prompt_cache_key_changed", "tools_changed",
	"text_format_changed", "reasoning_effort_changed", "verbosity_changed",
	"context_compacted", "input_changed", "service_tier_changed",
}

func cacheTraceDiagnosticEnum(value gjson.Result) string {
	if !value.Exists() {
		return "absent"
	}
	best := len(cacheTraceDiagnosticNames)
	consider := func(v gjson.Result) {
		if v.Type != gjson.String {
			return
		}
		for i, name := range cacheTraceDiagnosticNames {
			if v.Str == name && i < best {
				best = i
			}
		}
	}
	if value.IsArray() {
		value.ForEach(func(_, item gjson.Result) bool { consider(item); return true })
	} else {
		consider(value)
	}
	if best < len(cacheTraceDiagnosticNames) {
		return cacheTraceDiagnosticNames[best]
	}
	return "other"
}

func cacheTraceResponseTTLEnum(value gjson.Result) string {
	if !value.Exists() {
		return "absent"
	}
	switch value.Str {
	case "30m":
		return "30m"
	case "12h":
		return "12h"
	case "24h":
		return "24h"
	case "in_memory":
		return "in_memory"
	default:
		return "other"
	}
}

func cacheTraceResponseContextEnum(value gjson.Result) string {
	switch value.Str {
	case "current_turn":
		return "current_turn"
	case "all_turns":
		return "all_turns"
	default:
		return "unknown"
	}
}

// Match firstUsageCount validity without reparsing the response. The parser
// clamps an explicit nonpositive number to zero but retains its presence.
func cacheTraceUsageCountPresent(value gjson.Result) bool {
	return value.Type == gjson.Number && !math.IsNaN(value.Num) && !math.IsInf(value.Num, 0) &&
		value.Num == math.Trunc(value.Num) && value.Num < float64(math.MaxInt)
}

func cacheTraceUsagePresence(u gjson.Result) (bits uint8, prompt bool) {
	if !u.IsObject() {
		return 0, false
	}
	prompt = cacheTraceUsageCountPresent(u.Get("input_tokens")) || cacheTraceUsageCountPresent(u.Get("prompt_tokens"))
	for bit, paths := range [...][]string{
		{"input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cached_tokens", "cache_read_tokens"},
		{"prompt_cache_hit_tokens"}, {"cache_read_input_tokens"}, {"cachedContentTokenCount"},
		{"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens"},
		{"prompt_cache_diagnostics"},
		{"cache_write_tokens_details.ttl_30m", "input_tokens_details.cache_write_tokens_details.ttl_30m", "prompt_tokens_details.cache_write_tokens_details.ttl_30m", "cache_write_tokens_details.ttl_30m_tokens", "input_tokens_details.cache_write_tokens_details.ttl_30m_tokens", "prompt_tokens_details.cache_write_tokens_details.ttl_30m_tokens"},
		{"cache_write_tokens_details.ttl_12h", "input_tokens_details.cache_write_tokens_details.ttl_12h", "prompt_tokens_details.cache_write_tokens_details.ttl_12h", "cache_write_tokens_details.ttl_12h_tokens", "input_tokens_details.cache_write_tokens_details.ttl_12h_tokens", "prompt_tokens_details.cache_write_tokens_details.ttl_12h_tokens"},
	} {
		for _, path := range paths {
			value := u.Get(path)
			if bit < 5 && value.Type == gjson.Number || bit >= 5 && value.Exists() {
				bits |= 1 << bit
				break
			}
		}
	}
	return bits, prompt
}

// The buffered compatibility path decodes outer usage/details fields with
// encoding/json's case-insensitive matching. Its counts still use the original
// gjson parser, but a typed cached_tokens detail survives when no exact-case
// alias overwrote it. Observe those already retained small usage views too.
// The caller unions only the root/response view actually chosen by billing.
func cacheTraceTypedUsageFields(u gjson.Result) uint8 {
	bits, _ := cacheTraceUsagePresence(u)
	if !u.IsObject() {
		return bits
	}
	u.ForEach(func(key, value gjson.Result) bool {
		if !strings.EqualFold(key.Str, "input_tokens_details") && !strings.EqualFold(key.Str, "prompt_tokens_details") {
			return true
		}
		if value.IsObject() {
			value.ForEach(func(name, count gjson.Result) bool {
				if strings.EqualFold(name.Str, "cached_tokens") && cacheTraceUsageCountPresent(count) {
					bits |= 1
				}
				return true
			})
		}
		return true
	})
	return bits
}

func (index *cacheTraceResponseIndex) observation(slot int) cacheTraceResponseObservation {
	result := cacheTraceResponseObservation{reasoning: "unknown", diagnostics: "absent", ttl: "absent"}
	var ttl, retention gjson.Result
	// Metadata is independent of the envelope selected for billing usage.
	// For each metadata path keep the same root/response/data/data.response
	// precedence; an explicit null claims its path and does not fall through.
	for i := range index.candidates {
		fields := &index.candidates[i]
		bits, _ := cacheTraceUsagePresence(fields.usage)
		result.usageFields |= bits
		if fields.seen&cacheTraceIndexedDiagnostics != 0 {
			result.usageFields |= 1 << 5
		}
		if result.metadataPresent&cacheTraceIndexedReasoning == 0 && fields.seen&cacheTraceIndexedReasoning != 0 {
			result.metadataPresent |= cacheTraceIndexedReasoning
			result.reasoning = cacheTraceResponseContextEnum(fields.reasoningCtx)
		}
		if result.metadataPresent&cacheTraceIndexedDiagnostics == 0 && fields.seen&cacheTraceIndexedDiagnostics != 0 {
			result.metadataPresent |= cacheTraceIndexedDiagnostics
			result.diagnostics = cacheTraceDiagnosticEnum(fields.diagnostics)
		}
		if result.metadataPresent&cacheTraceIndexedTTL == 0 && fields.seen&cacheTraceIndexedTTL != 0 {
			result.metadataPresent |= cacheTraceIndexedTTL
			ttl = fields.cacheTTL
		}
		if result.metadataPresent&cacheTraceIndexedRetention == 0 && fields.seen&cacheTraceIndexedRetention != 0 {
			result.metadataPresent |= cacheTraceIndexedRetention
			retention = fields.cacheRetention
		}
	}
	// New ttl wins over legacy retention even when they live in different
	// supported envelopes. Both views were collected in the same index pass.
	if result.metadataPresent&cacheTraceIndexedTTL != 0 {
		result.ttl = cacheTraceResponseTTLEnum(ttl)
	} else if result.metadataPresent&cacheTraceIndexedRetention != 0 {
		result.ttl = cacheTraceResponseTTLEnum(retention)
	}
	for i := range index.typedUsage {
		_, result.typedPrompt[i] = cacheTraceUsagePresence(index.typedUsage[i])
		result.typedUsageFields[i] = cacheTraceTypedUsageFields(index.typedUsage[i])
	}
	if slot >= 0 && slot < len(index.candidates) {
		fields := &index.candidates[slot]
		result.usagePresent = fields.usage.IsObject()
		_, result.promptPresent = cacheTraceUsagePresence(fields.usage)
	}
	return result
}
