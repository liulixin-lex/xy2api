package service

import (
	"bytes"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Observation shares the existing billing traversal. Billing converters and
// merge rules remain unchanged; disabled requests use the original parser.
type cacheTraceUsageFold uint8

const (
	cacheTraceUsageKeep cacheTraceUsageFold = iota
	cacheTraceUsageMerge
	cacheTraceUsageReplace
)

func extractOpenAIUsageIndexed(body []byte) (OpenAIUsage, cacheTraceResponseObservation, bool) {
	index, valid := indexCacheTraceResponse(body)
	if !valid {
		return OpenAIUsage{}, cacheTraceResponseObservation{}, false
	}
	usage, slot, ok := index.usage()
	return usage, index.observation(slot), ok
}

func (s *OpenAIGatewayService) parseSSEUsageBytesWithTypeIndexed(data []byte, eventType string, usage *OpenAIUsage, typed ...bool) (cacheTraceResponseObservation, cacheTraceUsageFold, bool) {
	if usage == nil || len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return cacheTraceResponseObservation{}, cacheTraceUsageKeep, false
	}
	hasBillingUsage := bytes.Contains(data, []byte(`"usage"`))
	if !hasBillingUsage && !cacheTraceResponseMetadataHint(data) && (len(typed) == 0 || !typed[0]) {
		return cacheTraceResponseObservation{}, cacheTraceUsageKeep, false
	}
	parsedUsage, observation, ok := extractOpenAIUsageIndexed(data)
	if !ok || !hasBillingUsage {
		return observation, cacheTraceUsageKeep, observation.usageFields != 0 || observation.metadataPresent != 0 || len(typed) > 0 && typed[0]
	}
	if openAIStreamEventTypeIsTerminal(effectiveOpenAISSEEventType(data, eventType)) {
		if !openAIUsageHasTokens(&parsedUsage) && openAIUsageHasTokens(usage) {
			return observation, cacheTraceUsageKeep, true
		}
		*usage = parsedUsage
		return observation, cacheTraceUsageReplace, true
	}
	mergeOpenAIUsageNonZero(usage, parsedUsage)
	return observation, cacheTraceUsageMerge, true
}

// Metadata-only response frames still matter when usage is absent or was
// reported in an earlier frame. These two lexical checks allocate nothing and
// do not validate/parse ordinary delta JSON. Keep hasBillingUsage separate so
// escaped usage keys cannot bypass the original billing fast guard.
func cacheTraceResponseMetadataHint(data []byte) bool {
	if bytes.Contains(data, []byte(`"reasoning"`)) {
		return true
	}
	// Match full known keys after the common prefix. An escaped quote in
	// ordinary delta text must not turn a field-name mention into metadata.
	for rest := data; ; {
		at := bytes.Index(rest, []byte(`"prompt_cache_`))
		if at < 0 {
			return false
		}
		rest = rest[at+len(`"prompt_cache_`):]
		if bytes.HasPrefix(rest, []byte(`diagnostics"`)) || bytes.HasPrefix(rest, []byte(`options"`)) || bytes.HasPrefix(rest, []byte(`retention"`)) {
			return true
		}
	}
}

// Raw CC intentionally has no ValidBytes validation and only examines the
// top-level usage path. Keep that distinct from Responses, including malformed
// or half frames that its existing gjson.Get may still accept.
func extractCCStreamUsageIndexed(payload string) (*OpenAIUsage, cacheTraceResponseObservation) {
	view := gjson.Get(payload, "usage")
	usage, ok := openAIUsageFromGJSON(view)
	if !ok {
		return nil, cacheTraceResponseObservation{}
	}
	bits, prompt := cacheTraceUsagePresence(view)
	return &usage, cacheTraceResponseObservation{usageFields: bits, promptPresent: prompt, usagePresent: true,
		reasoning: "unknown", diagnostics: "absent", ttl: "absent"}
}

func (t *cacheTrace) observeCacheTraceParsedResponse(observation cacheTraceResponseObservation, fold cacheTraceUsageFold) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.data.UsageFields |= observation.usageFields
	if observation.usagePresent {
		t.data.UsagePromptObserved = true
		switch fold {
		case cacheTraceUsageReplace:
			t.data.UsagePromptPresent = observation.promptPresent
		case cacheTraceUsageMerge:
			t.data.UsagePromptPresent = t.data.UsagePromptPresent || observation.promptPresent
		}
	}
	// Retain only normalized literals. Later explicit response declarations
	// override earlier ones; absence does not erase a concrete declaration.
	if observation.metadataPresent&cacheTraceIndexedReasoning != 0 || observation.reasoning != "" && observation.reasoning != "unknown" {
		t.data.ReasoningContextEffective = observation.reasoning
	}
	if observation.diagnostics != "" && observation.diagnostics != "absent" {
		t.data.PromptCacheDiagnostics = observation.diagnostics
	}
	if observation.ttl != "" && observation.ttl != "absent" {
		t.data.ResponseCacheTTL = observation.ttl
	}
	t.mu.Unlock()
}

type cacheTraceTypedUsagePresence struct {
	root, response             bool
	rootFields, responseFields uint8
}

func (s *OpenAIGatewayService) parseSSEUsageBytesWithTypeForCacheTrace(c *gin.Context, data []byte, eventType string, usage *OpenAIUsage, typed ...bool) cacheTraceTypedUsagePresence {
	t := cacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		s.parseSSEUsageBytesWithType(data, eventType, usage)
		return cacheTraceTypedUsagePresence{}
	}
	observation, fold, observed := s.parseSSEUsageBytesWithTypeIndexed(data, eventType, usage, typed...)
	if observed {
		t.observeCacheTraceParsedResponse(observation, fold)
	}
	return cacheTraceTypedUsagePresence{root: observation.typedPrompt[0], response: observation.typedPrompt[1],
		rootFields: observation.typedUsageFields[0], responseFields: observation.typedUsageFields[1]}
}

func extractOpenAIUsageForCacheTrace(c *gin.Context, body []byte) (OpenAIUsage, bool) {
	if !cacheTraceObserving(c) {
		return extractOpenAIUsageFromJSONBytes(body)
	}
	usage, observation, ok := extractOpenAIUsageIndexed(body)
	cacheTraceFromGin(c).observeCacheTraceParsedResponse(observation, cacheTraceUsageReplace)
	return usage, ok
}

func extractCCStreamUsageForCacheTrace(c *gin.Context, payload string) *OpenAIUsage {
	t := cacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		return extractCCStreamUsage(payload)
	}
	usage, observation := extractCCStreamUsageIndexed(payload)
	if usage != nil {
		t.observeCacheTraceParsedResponse(observation, cacheTraceUsageReplace)
	}
	return usage
}

// The buffered Chat->Responses terminal later overwrites gjson usage from its
// already decoded event. Match that final selection using scalars from the
// same index; never rescan payload or change typed billing assignments.
func (t *cacheTrace) observeCacheTraceTypedPrompt(presence cacheTraceTypedUsagePresence, fromRoot bool) {
	if t == nil {
		return
	}
	present, fields := presence.response, presence.responseFields
	if fromRoot {
		present, fields = presence.root, presence.rootFields
	}
	t.mu.Lock()
	t.data.UsagePromptObserved = true
	t.data.UsagePromptPresent = present
	t.data.UsageFields |= fields
	t.mu.Unlock()
}
