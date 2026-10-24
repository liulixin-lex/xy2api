package service

import (
	"github.com/tidwall/gjson"
	"strings"
)

// DRAFT ONLY: this file is not wired into the product. Call only from the
// trace-enabled branch; the disabled branch must keep calling the original
// extractOpenAIUsageFromJSONBytes. The returned views are borrowed from body,
// consumed synchronously, and must never be stored on a trace or emitted.
//
// A gjson.Result is a raw view, not an indexed object. Index each relevant
// envelope once, then use only these small subtrees/leaves. In particular, do
// not call Get on the complete response again for each observation field.
// gjson.ForEach still skips a nested envelope while identifying its boundary;
// its nested traversal visits that envelope again. Thus this is one pass per
// envelope, not a claim of a single lexical visit to every output byte.
type cacheTraceResponseIndex struct {
	// Same precedence as extractOpenAIUsageFromJSONBytes:
	// usage, response.usage, data.usage, data.response.usage.
	candidates [4]cacheTraceResponseFields
	// encoding/json overwrites the usage pointer at each encountered usage field,
	// while duplicate response objects merge unless response:null resets it.
	typedUsage [2]gjson.Result // root / response; transient small views only
}

type cacheTraceResponseFields struct {
	usage          gjson.Result
	imageGen       gjson.Result // tool_usage.image_gen
	reasoningCtx   gjson.Result // reasoning.context
	diagnostics    gjson.Result // prompt_cache_diagnostics
	cacheTTL       gjson.Result // prompt_cache_options.ttl
	cacheRetention gjson.Result // prompt_cache_retention
	seen           uint8
}

const (
	cacheTraceIndexedUsage uint8 = 1 << iota
	cacheTraceIndexedImageGen
	cacheTraceIndexedReasoning
	cacheTraceIndexedDiagnostics
	cacheTraceIndexedTTL
	cacheTraceIndexedRetention
)

// indexCacheTraceResponse preserves first *complete path* semantics, including
// explicit null. For duplicate envelope keys, gjson.Get("response.usage") may
// continue to a later response object if the first has no usage key; simply
// retaining the first response object would silently change billing. Every
// duplicate envelope therefore contributes only still-missing leaf paths.
func indexCacheTraceResponse(body []byte) (cacheTraceResponseIndex, bool) {
	var index cacheTraceResponseIndex
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return index, false
	}
	index.indexEnvelope(parseRawJSONView(body), 0)
	return index, true
}

func (index *cacheTraceResponseIndex) indexEnvelope(object gjson.Result, slot int) {
	if !object.IsObject() {
		return
	}
	fields := &index.candidates[slot]
	object.ForEach(func(key, value gjson.Result) bool {
		if (slot == 0 || slot == 1) && strings.EqualFold(key.Str, "usage") {
			index.typedUsage[slot] = value
		}
		if slot == 0 && strings.EqualFold(key.Str, "response") {
			if value.Type == gjson.Null {
				index.typedUsage[1] = gjson.Result{}
			}
			if key.Str != "response" && value.IsObject() {
				index.indexTypedResponse(value)
			}
		}
		switch key.Str {
		case "usage":
			fields.keepFirst(cacheTraceIndexedUsage, &fields.usage, value)
		case "tool_usage":
			fields.keepFirstLeaf(value, "image_gen", cacheTraceIndexedImageGen, &fields.imageGen)
		case "reasoning":
			fields.keepFirstLeaf(value, "context", cacheTraceIndexedReasoning, &fields.reasoningCtx)
		case "prompt_cache_diagnostics":
			fields.keepFirst(cacheTraceIndexedDiagnostics, &fields.diagnostics, value)
		case "prompt_cache_options":
			fields.keepFirstLeaf(value, "ttl", cacheTraceIndexedTTL, &fields.cacheTTL)
		case "prompt_cache_retention":
			fields.keepFirst(cacheTraceIndexedRetention, &fields.cacheRetention, value)
		case "response":
			switch slot {
			case 0:
				index.indexEnvelope(value, 1)
			case 2:
				index.indexEnvelope(value, 3)
			}
		case "data":
			if slot == 0 {
				index.indexEnvelope(value, 2)
			}
		}
		return true
	})
}

func (fields *cacheTraceResponseFields) keepFirst(bit uint8, destination *gjson.Result, value gjson.Result) {
	if fields.seen&bit == 0 {
		*destination = value
		fields.seen |= bit
	}
}

func (fields *cacheTraceResponseFields) keepFirstLeaf(object gjson.Result, key string, bit uint8, destination *gjson.Result) {
	if fields.seen&bit != 0 || !object.IsObject() {
		return
	}
	object.ForEach(func(name, value gjson.Result) bool {
		if name.Str == key {
			fields.keepFirst(bit, destination, value)
			return false
		}
		return true
	})
}

// usage uses the original billing converters. It neither normalizes aliases
// itself nor changes terminal/progressive usage accumulation at its caller.
// The winning slot lets the observer select metadata from the same envelope;
// non-winning candidates remain available for presence-only bitmap union.
func (index *cacheTraceResponseIndex) usage() (OpenAIUsage, int, bool) {
	for slot := range index.candidates {
		fields := &index.candidates[slot]
		if usage, ok := openAIUsageFromGJSON(fields.usage); ok {
			mergeHostedImageGenToolUsage(fields.imageGen, &usage)
			return usage, slot, true
		}
	}
	return OpenAIUsage{}, -1, false
}

// Case-insensitive field matching is specific to encoding/json. Do not fill
// the case-sensitive gjson billing candidates from this compatibility view.
func (index *cacheTraceResponseIndex) indexTypedResponse(object gjson.Result) {
	object.ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.Str, "usage") {
			index.typedUsage[1] = value
		}
		return true
	})
}
