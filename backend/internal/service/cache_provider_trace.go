package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// BeginProviderCacheTrace freezes the request policy before session derivation
// and selection. parsed is optional (Gemini CLI already has a local hash).
// Excluded requests never create state or install a source sink.
func (s *GatewayService) BeginProviderCacheTrace(c *gin.Context, platform, protocol string, body []byte, parsed *ParsedRequest) {
	if s == nil || s.providerTraceEngine == nil {
		return
	}
	t := s.providerTraceEngine.beginProviderCacheTrace(c, platform, protocol, body)
	if t != nil && parsed != nil {
		parsed.cacheTraceSource = &t.data.IDSource
	}
}

// BindProviderCacheTraceSession consumes the existing routing result; it never
// derives or changes routing identity. A nonempty source overrides the optional
// source sink (Gemini CLI). An unsupported source is explicitly unknown.
func (s *GatewayService) BindProviderCacheTraceSession(c *gin.Context, localSessionHash, source string) {
	if s == nil || s.providerTraceEngine == nil {
		return
	}
	t := cacheTraceFromGin(c)
	if t == nil || t.data.Platform == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if source != "" {
		t.data.IDSource = source
	}
	if t.data.Platform == PlatformGemini {
		switch t.data.IDSource {
		case "gemini_cli", "gemini_digest", "cacheable_content", "message_content_fallback", "anchor_v2":
		default:
			t.data.IDSource = "unknown"
		}
	}
	t.providerLocalSession = localSessionHash
	t.providerKey = s.providerTraceEngine.cacheProviderSessionScope(t.data.Platform, t.apiKeyID, t.data.GroupID, localSessionHash)
	t.sessionKey = t.providerKey.digest
	t.data.SessionDigest = t.sessionKey
	t.cohort = cacheTraceChainCohort(t.sessionKey)
	t.providerSampled = cacheTraceRequestSampled(c, t.policy, t.sessionKey)
	if t.sessionKey == "" {
		t.data.GapSource = "no_session"
	}
}

// ProviderCacheTraceSessionDigest is the only observation identity exposed to
// provider extensions, for observation only. It is already tenant/serving scoped.
// The same value indexes history/LRU and S2/S6; no raw hash escapes.
func ProviderCacheTraceSessionDigest(ctx context.Context) string {
	t := cacheTraceFrom(ctx)
	if t == nil || t.data.Platform == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.providerKey.digest
}

func providerCacheTraceFromGin(c *gin.Context) *cacheTrace {
	t := cacheTraceFromGin(c)
	if t == nil || !t.providerSampled || (t.data.Platform != PlatformAnthropic && t.data.Platform != PlatformGemini) {
		return nil
	}
	return t
}

func (s *OpenAIGatewayService) beginProviderCacheTrace(c *gin.Context, platform, protocol string, body []byte) *cacheTrace {
	if s == nil || s.cfg == nil || s.cfg.JWT.Secret == "" || c == nil || c.Request == nil || (platform != PlatformAnthropic && platform != PlatformGemini) {
		return nil
	}
	policy := s.CaptureCachePolicy(c)
	if !policy.TraceEnabledFor(platform) || getAPIKeyIDFromContext(c) <= 0 || monitorProbeRequest(c) {
		return nil
	}
	if protocol != "messages" && protocol != "chat" && protocol != "responses" && protocol != "gemini" {
		return nil
	}
	// Bind freezes sampling on the scoped session before scheduling. Business
	// counters remain independent of trace sampling, matching the OpenAI path.
	t := &cacheTrace{policy: policy, ingress: body, apiKeyID: getAPIKeyIDFromContext(c), data: cacheTraceData{
		Platform: platform, ProtocolIn: protocol, GroupID: getOpenAIGroupIDFromContext(c), PolRev: policy.Revision(),
		IDSource: "unknown", ConvAnchor: "unknown", UAFamily: cacheProviderUAFamily(c.Request.UserAgent()),
		Transport: "http", GapSource: "no_prev", RouteReason: "unknown", RebindCause: "none", TokensPerByteMilli: -1,
		Prefix: cachePrefixUnavailable(0), Provider: cacheProviderFieldsUnavailable(platform),
	}}
	if platform == PlatformAnthropic && policy.anchorPolicy.metadataAvailable {
		t.data.L03Present = true
		t.data.L03Arm = "none"
		t.data.L03Skip = "policy_off"
	}
	if platform == PlatformAnthropic && policy.l07Available {
		// trace m4l07: every full Anthropic event of an l07 build has l07_*.
		t.data.L07Present = true
		t.data.L07 = L07TraceFields{Arm: "none", Abandon: "none"}
	}
	if policy.l04Available {
		t.data.L04Present = true
		t.data.L04Arm = "none"
		t.data.L04Skip = "policy_off"
		t.data.L04PolicyRevision = policy.revs[4]
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, t))
	noteCacheTraceFallback(c.Request.Context())
	return t
}

func cacheProviderUAFamily(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case strings.Contains(u, "claude-code"), strings.Contains(u, "claude-cli"):
		return "claude_code"
	case strings.Contains(u, "anthropic"):
		return "anthropic_sdk"
	case strings.Contains(u, "gemini-cli"):
		return "gemini_cli"
	case strings.Contains(u, "google-genai"), strings.Contains(u, "google-api"):
		return "google_sdk"
	}
	return cacheTraceUAFamily(ua)
}

func (s *OpenAIGatewayService) captureProviderCacheTrace(c *gin.Context, req *http.Request, account *Account, protocol string) {
	t := cacheTraceFromGin(c)
	if t == nil || t.data.Platform == "" || req == nil || req.URL == nil || account == nil || account.Platform != t.data.Platform {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.GroupID = getOpenAIGroupIDFromContext(c)
	if t.providerServingGroupID > 0 {
		t.data.GroupID = t.providerServingGroupID
	}
	t.providerKey = s.cacheProviderSessionScope(t.data.Platform, t.apiKeyID, t.data.GroupID, t.providerLocalSession)
	t.sessionKey = t.providerKey.digest
	t.data.SessionDigest = t.sessionKey
	t.cohort = cacheTraceChainCohort(t.sessionKey)
	t.providerHost = req.URL.Hostname()
	t.data.AccountID = account.ID
	t.data.ProtocolOut = protocol
	if !t.providerSampled || req.GetBody == nil {
		return
	}
	t.pending.cancelPending()
	t.pending = nil
	t.async = nil
	t.chain = cachePrefixChain{}
	t.usageObservationFailed = false
	t.data.AccountID = account.ID
	t.data.ProtocolOut = protocol
	t.data.UsageFields = 0
	t.data.UsagePromptPresent = false
	t.data.UsagePromptObserved = true
	t.data.Provider.ThinkingDropped = false
	t.data.Prefix = cachePrefixUnavailable(int(req.ContentLength))
	t.data.NAReason = ""
	pool := s.cacheTraceWorkerPool()
	cost, ok := pool.reserve(int64(len(t.ingress)), req.ContentLength)
	if !ok {
		t.skipUsage = true
		t.data.NAReason = "budget_full"
		return
	}
	t.skipUsage = false
	t.async = &cacheTraceAsync{chainExcluded: t.cohort >= t.policy.ChainPercent(), providerKey: t.providerKey}
	t.pending = &cacheTraceJob{policy: t.policy, pool: pool, cost: cost, state: t.async, service: s,
		ingress: t.ingress, apiKeyID: t.apiKeyID, session: t.sessionKey, protocol: protocol, transport: "http",
		providerKey: t.providerKey, providerPlatform: t.data.Platform, providerProtocolIn: t.data.ProtocolIn, getBody: req.GetBody, size: req.ContentLength, created: time.Now()}
}

func (s *OpenAIGatewayService) markProviderCacheTraceSent(c *gin.Context) {
	t := cacheTraceFromGin(c)
	if t == nil || !t.providerSampled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data.AccountID == 0 {
		return
	}
	sequence := t.timing.sequence
	if sequence == 0 {
		sequence = cacheTraceSendSequence.Add(1)
	}
	t.timing = s.providerHistory.send(t.providerKey, sequence, time.Now(), s.cacheTraceCaps())
	if t.pending != nil {
		t.pending.history = t.timing.owner
		t.pending.sentAt = t.timing.sentAt
	}
}

// Submit the reserved job only after Do has returned response headers.
func (s *OpenAIGatewayService) submitCacheTraceJob(c *gin.Context) {
	t := cacheTraceFromGin(c)
	if t == nil || !t.providerSampled {
		return
	}
	t.mu.Lock()
	job := t.pending
	t.pending = nil
	t.mu.Unlock()
	if job != nil {
		s.cacheTraceWorkerPool().submit(job)
	}
}

func (s *OpenAIGatewayService) finishProviderCacheTrace(c *gin.Context, result *ForwardResult) {
	t := cacheTraceFromGin(c)
	if t == nil || t.data.Platform == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if result != nil {
		result.providerBusiness = &cacheProviderBusiness{platform: t.data.Platform, host: t.providerHost, session: t.sessionKey}
	}
	if !t.providerSampled {
		return
	}
	t.pending.cancelPending()
	t.pending = nil
	if result == nil {
		s.providerHistory.settle(t.providerKey, t.timing, time.Now())
		return
	}
	if t.data.AccountID == 0 || t.data.ProtocolOut == "" {
		return
	}
	t.applyCacheTraceComputed()
	if t.data.L07Present {
		// The selection of this forward has run; its L-07 record is final.
		t.data.L07 = L07TraceFieldsFromContext(c.Request.Context(), t.data.Provider.OwnerSkip)
	}
	data := t.data
	data.RequestIDHMAC = s.cacheIdentityDigest(data.Platform+"-request", result.RequestID)
	result.providerTrace = &cacheTrace{policy: t.policy, data: data, providerKey: t.providerKey, sessionKey: t.sessionKey, async: t.async, timing: t.timing, chain: t.chain, usageObservationFailed: t.usageObservationFailed}
}

// Called only behind a provider trace guard, on raw upstream payloads before
// translation. Presence is separate from the unchanged billing precedence.
func observeProviderCacheTrace(c *gin.Context, body []byte) {
	t := providerCacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		return
	}
	root := parseRawJSONView(body)
	var bits uint8
	present := false
	for _, path := range []string{"usage", "message.usage", "usageMetadata", "response.usageMetadata"} {
		u := root.Get(path)
		if !u.IsObject() {
			continue
		}
		if v := u.Get("cache_read_input_tokens"); cacheTraceUsageCountPresent(v) {
			bits |= 1
		}
		if v := u.Get("cached_tokens"); cacheTraceUsageCountPresent(v) {
			bits |= 1
		}
		if v := u.Get("cache_creation_input_tokens"); cacheTraceUsageCountPresent(v) {
			bits |= 2
		}
		if cacheTraceUsageCountPresent(u.Get("cache_creation.ephemeral_5m_input_tokens")) || cacheTraceUsageCountPresent(u.Get("cache_creation.ephemeral_1h_input_tokens")) {
			bits |= 4
		}
		if v := u.Get("cachedContentTokenCount"); cacheTraceUsageCountPresent(v) {
			bits |= 8
		}
		present = present || cacheTraceUsageCountPresent(u.Get("input_tokens")) || cacheTraceUsageCountPresent(u.Get("promptTokenCount"))
	}
	dropped := root.Get("thinking_dropped").Exists() || root.Get("message.thinking_dropped").Exists()
	t.mu.Lock()
	t.data.UsageFields |= bits
	t.data.UsagePromptPresent = t.data.UsagePromptPresent || present
	t.data.UsagePromptObserved = true
	t.data.Provider.ThinkingDropped = t.data.Provider.ThinkingDropped || dropped
	t.mu.Unlock()
}

func observeProviderCacheTraceMap(c *gin.Context, event map[string]any) {
	t := providerCacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		return
	}
	// The existing billing traversal has already decoded this frame.
	u, _ := event["usage"].(map[string]any)
	if message, ok := event["message"].(map[string]any); ok {
		if m, ok := message["usage"].(map[string]any); ok {
			u = m
		}
	}
	var bits uint8
	_, p := parseSSEUsageInt(u["input_tokens"])
	if _, ok := parseSSEUsageInt(u["cache_read_input_tokens"]); ok {
		bits |= 1
	}
	if _, ok := parseSSEUsageInt(u["cached_tokens"]); ok {
		bits |= 1
	}
	if _, ok := parseSSEUsageInt(u["cache_creation_input_tokens"]); ok {
		bits |= 2
	}
	if cc, ok := u["cache_creation"].(map[string]any); ok {
		if _, ok := parseSSEUsageInt(cc["ephemeral_5m_input_tokens"]); ok {
			bits |= 4
		}
		if _, ok := parseSSEUsageInt(cc["ephemeral_1h_input_tokens"]); ok {
			bits |= 4
		}
	}
	_, dropped := event["thinking_dropped"]
	t.mu.Lock()
	t.data.UsageFields |= bits
	t.data.UsagePromptPresent = t.data.UsagePromptPresent || p
	t.data.UsagePromptObserved = true
	t.data.Provider.ThinkingDropped = t.data.Provider.ThinkingDropped || dropped
	t.mu.Unlock()
}

// Existing decisions supply enums; these functions never inspect routing or
// concurrency stores. Non-fault skips do not become fault rebinds.
func noteProviderCacheTraceOwnerSkip(ctx context.Context, reason string) {
	t := cacheTraceFrom(ctx)
	if t == nil || t.data.Platform == "" {
		return
	}
	switch reason {
	case "busy_wait_full", "busy_quota_full", "gate_check", "rpm_red", "model_unsupported", "other":
	default:
		return
	}
	t.mu.Lock()
	t.data.Provider.OwnerSkip = reason
	if reason == "busy_wait_full" || reason == "busy_quota_full" || reason == "gate_check" || reason == "rpm_red" {
		t.promoteCacheTraceRouteLocked("non_fault_rebind")
	}
	t.mu.Unlock()
}
