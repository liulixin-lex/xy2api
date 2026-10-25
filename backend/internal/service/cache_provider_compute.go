package service

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type cacheProviderFields struct {
	CCBPIn, CCBPInjected                                                        int
	SessionClass                                                                string
	ThinkingBlocks, FirstThinkingBlock, FirstThinkingByte, FirstThinkingRawByte int
	ThinkingConfig                                                              string
	ThinkingDropped                                                             bool
	OwnerSkip                                                                   string
	lastTurnOnly                                                                bool
}

func cacheProviderFieldsUnavailable(platform string) cacheProviderFields {
	return cacheProviderFields{CCBPIn: -1, CCBPInjected: -1, SessionClass: "none", ThinkingBlocks: -1,
		FirstThinkingBlock: -1, FirstThinkingByte: -1, FirstThinkingRawByte: -1, ThinkingConfig: "none", OwnerSkip: "none"}
}

// Visit only protocol cache-control locations, never arbitrary user content.
func cacheProviderBreakpoints(root gjson.Result) int {
	n := 0
	visit := func(v gjson.Result) {
		if v.Get("cache_control.type").Str == "ephemeral" {
			n++
		}
	}
	root.Get("tools").ForEach(func(_, v gjson.Result) bool { visit(v); return true })
	system := root.Get("system")
	if system.IsArray() {
		system.ForEach(func(_, v gjson.Result) bool { visit(v); return true })
	}
	root.Get("messages").ForEach(func(_, m gjson.Result) bool {
		m.Get("content").ForEach(func(_, v gjson.Result) bool { visit(v); return true })
		return true
	})
	return min(n, 4)
}

func (s *OpenAIGatewayService) computeProviderCacheTrace(j *cacheTraceJob, body []byte, cutoff time.Time) *cacheTraceComputed {
	r := &cacheTraceComputed{prefix: cachePrefixUnavailable(len(body)), provider: cacheProviderFieldsUnavailable(j.providerPlatform)}
	in, out := parseRawJSONView(j.ingress), parseRawJSONView(body)
	r.anchor = s.cacheProviderTraceIngressAnchor(j.apiKeyID, in, j.providerProtocolIn)
	if j.providerPlatform == PlatformAnthropic {
		r.provider.CCBPIn = cacheProviderBreakpoints(in)
		r.provider.CCBPInjected = max(0, cacheProviderBreakpoints(out)-r.provider.CCBPIn)
		r.provider.ThinkingBlocks = 0
		uid := in.Get("metadata.user_id").Str
		if strings.HasPrefix(strings.TrimSpace(uid), "{") {
			r.provider.SessionClass = "metadata_json"
		} else if uid != "" {
			r.provider.SessionClass = "metadata_legacy"
		}
		thinking := out.Get("thinking")
		effort := out.Get("output_config.effort")
		if thinking.Exists() || effort.Exists() {
			r.provider.ThinkingConfig = s.cacheIdentityDigest("anthropic-thinking", thinking.Raw+"|"+effort.Raw)[:16]
		}
		model := strings.ToLower(out.Get("model").Str)
		r.provider.lastTurnOnly = strings.Contains(model, "haiku") || strings.Contains(model, "claude-3") ||
			strings.Contains(model, "sonnet-4-0") || strings.Contains(model, "opus-4-0") || strings.Contains(model, "opus-4-1") ||
			model == "claude-sonnet-4" || model == "claude-opus-4"
	}
	if !cacheTraceChainSampled(j.policy, j.session) {
		return r
	}
	history := s.providerHistory.snapshot(j.providerKey, j.history, cutoff)
	chain, positions, o := scanCachePrefix(body, j.protocol, cachePrefixProcessSeed, nil, history)
	chain.anchor = r.anchor
	compareCachePrefix(chain, positions, history, &o)
	for _, old := range history {
		if old.blocks > 0 {
			o.HistoryUnsampled = false
			break
		}
	}
	if j.providerPlatform == PlatformAnthropic {
		out.Get("messages").ForEach(func(_, m gjson.Result) bool {
			m.Get("content").ForEach(func(_, b gjson.Result) bool {
				kind := b.Get("type").Str
				if kind != "thinking" && kind != "redacted_thinking" {
					return true
				}
				r.provider.ThinkingBlocks++
				if r.provider.FirstThinkingRawByte < 0 {
					r.provider.FirstThinkingRawByte = b.Index
					for i := 0; i < positions.n; i++ {
						if int(positions.raw[i]) == b.Index {
							r.provider.FirstThinkingBlock = i
							r.provider.FirstThinkingByte = int(positions.logical[i])
							break
						}
					}
				}
				return true
			})
			return true
		})
	}
	r.chain = limitCachePrefixCheckpoints(chain, s.cacheTraceCaps().MaxCheckpoints)
	r.prefix = o
	return r
}

// The anchor follows the actual ingress protocol, including Messages routed to
// Gemini and OpenAI-format requests routed to Anthropic. Outbound translation
// must not turn unrelated ingress conversations into a shared empty anchor.
func (s *OpenAIGatewayService) cacheProviderTraceIngressAnchor(apiKeyID int64, root gjson.Result, protocol string) string {
	switch protocol {
	case "gemini":
		return s.cacheProviderTraceAnchorParts(apiKeyID, root.Get("systemInstruction"), root.Get("contents"), PlatformGemini)
	case "responses":
		return s.cacheTraceAnchorParts(apiKeyID, root.Get("instructions"), root.Get("input"))
	default:
		return s.cacheTraceAnchorParts(apiKeyID, root.Get("system"), root.Get("messages"))
	}
}
