package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type cacheTraceKey struct{}

// The trace is request-local and contains only enums, counts and digests.
// A separate snapshot is attached to the forward result before async billing.
// Only the live request keeps the immutable ingress body; snapshots keep an
// atomic async result handle, never bodies, headers, or a Gin context.
type cacheTrace struct {
	policy                 *CachePolicySnapshot
	mu                     sync.Mutex
	data                   cacheTraceData
	sessionKey             string
	cohort                 int // stable session scalar, computed once before attempts
	emitted                bool
	settled                bool
	timing                 cacheTraceTimingToken
	routing                cacheTraceRoutingState
	window                 cacheTraceWindow
	usageObservationFailed bool
	skipUsage              bool // budget rejection: use the final billing usage only
	chain                  cachePrefixChain
	ingress                []byte
	apiKeyID               int64
	headerSource           bool
	pending                *cacheTraceJob
	async                  *cacheTraceAsync
}

type cacheTraceData struct {
	L01Arm, L01Skip                                                     string
	L01Applied                                                          bool
	L01PolicyRevision                                                   int64
	LossClass, NAReason                                                 string
	PromptTokens, ReadTokens                                            int
	Prefix                                                              cachePrefixObservation
	RequestIDHMAC, SessionDigest, ConvAnchor                            string
	ProtocolIn, ProtocolOut                                             string
	GroupID, AccountID                                                  int64
	PCKClassIn, PCKClassOut                                             string
	PCKOutIsUUID                                                        bool
	SessHdrIn, SessHdrOut                                               uint16
	SessOutRelation, IDSource                                           string
	ThreadPresent                                                       bool
	UsageFields                                                         uint8
	UsagePromptObserved, UsagePromptPresent                             bool
	ReasoningContextEffective, PromptCacheDiagnostics, ResponseCacheTTL string
	Transport                                                           string
	IngressWSBridge                                                     bool
	RouteReason, RebindCause                                            string
	PolRev, UAFamily, GapSource                                         string
	CodexHeaders                                                        uint8
	WindowSequenceDelta, GapSendMS                                      int64
	TokensPerByteMilli                                                  int
	ReasoningBoundaryHit, ReadShortOfFork                               bool
}

var cacheTraceUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var cacheTraceHeaders = [...]string{
	"session_id", "session-id", "conversation_id", "x-session-id",
	"x-session-affinity", "x-opencode-session", "x-conversation-id",
	"thread_id", "thread-id", "x-codex-turn-state", "x-codex-turn-metadata",
	"x-client-request-id", "x-claude-code-session-id", "x-grok-conv-id",
}

// Canonicalize the fixed names once. Header presence is checked several times
// per request; repeated CanonicalHeaderKey calls allocate for uncommon names.
var cacheTraceHeaderKeys = func() [len(cacheTraceHeaders)]string {
	var keys [len(cacheTraceHeaders)]string
	for i, name := range cacheTraceHeaders {
		keys[i] = http.CanonicalHeaderKey(name)
	}
	return keys
}()

func cacheTraceFrom(ctx context.Context) *cacheTrace {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(cacheTraceKey{}).(*cacheTrace)
	return t
}

func cacheTraceFromGin(c *gin.Context) *cacheTrace {
	if c == nil || c.Request == nil {
		return nil
	}
	return cacheTraceFrom(c.Request.Context())
}

func (s *OpenAIGatewayService) cacheTraceEnabled(c *gin.Context) bool {
	return s != nil && s.cfg != nil && s.cfg.JWT.Secret != "" && s.CaptureCachePolicy(c).TraceEnabledFor(cacheTraceOpenAIPlatform(c))
}

func cacheTraceHeaderBits(h http.Header) (bits uint16) {
	for i, name := range cacheTraceHeaderKeys {
		if _, ok := h[name]; ok {
			bits |= 1 << i
		}
	}
	return bits
}

func cacheTracePCKIn(value gjson.Result) string {
	if !value.Exists() {
		return "none"
	}
	if value.Type == gjson.String && value.Str == "" {
		return "empty"
	}
	if value.Type == gjson.String && cacheTraceUUID.MatchString(value.Str) {
		return "client_uuid"
	}
	return "client_non_uuid"
}

// Each part is length-delimited; no concatenation ambiguity or retained content.
func (s *OpenAIGatewayService) cacheTraceAnchorForKey(apiKeyID int64, root gjson.Result) string {
	var instructions, items, messages gjson.Result
	root.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "instructions":
			instructions = v
		case "input":
			items = v
		case "messages":
			messages = v
		}
		return true
	})
	if !items.Exists() {
		items = messages
	}
	return s.cacheTraceAnchorParts(apiKeyID, instructions, items)
}

func (s *OpenAIGatewayService) cacheTraceAnchorParts(apiKeyID int64, instructions, items gjson.Result) string {
	h := hmac.New(sha256.New, []byte(s.cfg.JWT.Secret))
	_, _ = io.WriteString(h, "cache-trace-anchor-v1\x00"+strconv.FormatInt(apiKeyID, 10))
	var size [8]byte
	part := func(raw string) {
		binary.BigEndian.PutUint64(size[:], uint64(len(raw)))
		_, _ = h.Write(size[:])
		_, _ = io.WriteString(h, raw)
	}
	part(instructions.Raw)
	if items.Type == gjson.String {
		part(items.Raw)
	} else {
		leading := true
		items.ForEach(func(_, item gjson.Result) bool {
			role := item.Get("role").Str
			if leading && (role == "system" || role == "developer") {
				part(item.Raw)
				return true
			}
			leading = false
			if role == "user" {
				part(item.Get("content").Raw)
				return false
			}
			return true
		})
	}
	var sum [32]byte
	return hex.EncodeToString(h.Sum(sum[:0])[:8])
}

func (s *OpenAIGatewayService) beginCacheTrace(c *gin.Context, body []byte) {
	// Gate before any JSON access, context creation, or allocation.
	if !s.cacheTraceEnabled(c) || c == nil || c.Request == nil || c.Request.URL == nil || getAPIKeyIDFromContext(c) <= 0 || isGrokRequestContext(c) || monitorProbeRequest(c) {
		return
	}
	// HTTP Responses (including compact), Chat and the explicitly enabled
	// Messages bridge share the OpenAI worker. WS turns remain out of scope.
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS || (!strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/responses/compact") && !strings.HasSuffix(path, "/chat/completions") && !strings.HasSuffix(path, "/messages")) {
		return
	}
	if key, ok := c.Get("api_key"); ok {
		if api, ok := key.(*APIKey); ok && api.Group != nil && api.Group.Platform != "" && api.Group.Platform != PlatformOpenAI {
			return
		}
	}
	session, _ := c.Get(openAICacheSessionGinKey)
	sessionKey, _ := session.(string)
	if !cacheTraceRequestSampled(c, CachePolicyFromGin(c), sessionKey) {
		return
	}
	if t := cacheTraceFromGin(c); t != nil {
		t.mu.Lock()
		t.sessionKey = sessionKey
		t.cohort = cacheTraceChainCohort(sessionKey)
		t.data.GroupID = getOpenAIGroupIDFromContext(c)
		t.data.SessionDigest = sessionKey
		t.resetL01Attempt()
		t.mu.Unlock()
		noteCacheTraceFallback(c.Request.Context())
		return
	}

	// Entire-body lookups (including a missing pck and the HMAC anchor) belong
	// to the worker. The request already owns this non-pooled immutable slice.
	bits := cacheTraceHeaderBits(c.Request.Header)
	source := "unknown"
	headerSource := explicitOpenAIHeaderSessionID(c) != ""
	if headerSource {
		source = "header"
	}
	protocol := "responses"
	if strings.HasSuffix(path, "/chat/completions") {
		protocol = "chat"
	} else if strings.HasSuffix(path, "/messages") {
		protocol = "messages"
	}
	gapSource := "no_prev"
	if sessionKey == "" {
		gapSource = "no_session"
	}
	t := &cacheTrace{policy: CachePolicyFromGin(c), sessionKey: sessionKey, cohort: cacheTraceChainCohort(sessionKey), ingress: body, apiKeyID: getAPIKeyIDFromContext(c), headerSource: headerSource, window: cacheTraceParseWindow(c.Request.Header.Get("X-Codex-Window-Id")), data: cacheTraceData{
		SessionDigest: sessionKey, GroupID: getOpenAIGroupIDFromContext(c), ConvAnchor: "unknown",
		PolRev: CachePolicyFromGin(c).Revision(), UAFamily: cacheTraceUAFamily(c.Request.UserAgent()), CodexHeaders: cacheTraceCodexHeaderBits(c.Request.Header), WindowSequenceDelta: -1, GapSource: gapSource, TokensPerByteMilli: -1,
		ProtocolIn: protocol, PCKClassIn: "unknown", PCKClassOut: "unknown",
		SessHdrIn: bits, SessOutRelation: "none", IDSource: source,
		ThreadPresent: bits&((1<<7)|(1<<8)) != 0,
		Transport:     "http", RouteReason: "unknown", RebindCause: "none", Prefix: cachePrefixUnavailable(0),
		ReasoningContextEffective: "unknown", PromptCacheDiagnostics: "absent", ResponseCacheTTL: "absent",
	}}
	t.resetL01Attempt()
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, t))
	noteCacheTraceFallback(c.Request.Context())
}

func (t *cacheTrace) setPCKSource(source string) {
	if t == nil {
		return
	}
	switch source {
	case "none", "passthrough", "gw_compat_hex", "gw_from_header", "gw_grok", "gw_uuid", "oauth_rewritten":
	default:
		return
	}
	t.mu.Lock()
	t.data.PCKClassOut = source
	t.mu.Unlock()
}

func markCacheTracePCK(traces []*cacheTrace, source string) {
	if len(traces) > 0 {
		traces[0].setPCKSource(source)
	}
}

func (t *cacheTrace) setSessionSource(source string) {
	if t == nil {
		return
	}
	switch source {
	case "none", "client_value", "uuid_of_pck", "gw_isolated", "fingerprint_converged", "eq_pck":
	default:
		return
	}
	t.mu.Lock()
	t.data.SessOutRelation = source
	t.mu.Unlock()
}

// Requires t.mu or a newly created trace. A request keeps its policy snapshot
// across retries; account eligibility and wire application belong to each
// attempt. Uninstrumented protocols therefore cannot inherit a prior success.
func (t *cacheTrace) resetL01Attempt() {
	p := t.policy.UpstreamIdentity()
	t.data.L01Arm, t.data.L01Applied = "none", false
	t.data.L01Skip, t.data.L01PolicyRevision = "policy_off", p.Revision()
	if p.Enabled() {
		t.data.L01Skip = "unsupported_ingress"
	}
}

// Forward attempts share ingress metadata, but must not carry output or usage
// from an earlier failed account. This is also safe for protocol fallback.
func resetCacheTraceAttempt(c *gin.Context) {
	t := cacheTraceFromGin(c)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetL01Attempt()
	t.data.PCKClassOut = "unknown"
	if t.data.PCKClassIn == "none" {
		t.data.PCKClassOut = "none"
	} else if t.data.PCKClassIn != "unknown" {
		t.data.PCKClassOut = "passthrough"
	}
	t.data.PCKOutIsUUID = false
	t.data.SessHdrOut = 0
	t.data.SessOutRelation = "none"
	t.data.UsageFields = 0
	t.data.LossClass, t.data.NAReason = "", ""
	t.data.PromptTokens, t.data.ReadTokens = 0, 0
	t.data.TokensPerByteMilli = -1
	t.data.ReasoningBoundaryHit, t.data.ReadShortOfFork = false, false
	t.data.GapSource, t.data.GapSendMS = "no_prev", 0
	if t.sessionKey == "" {
		t.data.GapSource = "no_session"
	}
	t.data.UsagePromptObserved, t.data.UsagePromptPresent = false, false
	t.data.ReasoningContextEffective, t.data.PromptCacheDiagnostics, t.data.ResponseCacheTTL = "unknown", "absent", "absent"
	t.usageObservationFailed = false
	t.data.AccountID = 0
	t.data.ProtocolOut = ""
	t.data.Transport = "http"
	t.data.Prefix = cachePrefixUnavailable(0)
	t.pending.cancelPending()
	t.pending = nil
	t.skipUsage = false
	t.async = nil
	t.chain = cachePrefixChain{}
}

func (s *OpenAIGatewayService) captureCacheTraceRequest(c *gin.Context, req *http.Request, account *Account, protocol string) {
	t := cacheTraceFromGin(c)
	if t == nil || account == nil || account.Platform != PlatformOpenAI || req == nil || req.GetBody == nil ||
		(t.data.ProtocolIn == "messages" && protocol != "responses") {
		return
	}

	t.mu.Lock()
	// Free an unsent/replaced attempt before trying to own the final wire body.
	t.pending.cancelPending()
	t.pending = nil
	t.skipUsage = false
	pool := s.cacheTraceWorkerPool()
	cost, admitted := pool.reserve(int64(len(t.ingress)), req.ContentLength)
	chainExcluded := t.cohort >= t.policy.ChainPercent()
	t.data.NAReason = ""
	t.async = nil

	t.data.AccountID = account.ID
	t.data.ProtocolOut = protocol
	// Responses passthrough returns before the L-01 injection decision. Keep
	// its exclusion precise using only the selected account's existing flags.
	// policy_off and raw Chat exclusions retain their earlier priority.
	if t.data.L01Skip == "unsupported_ingress" && protocol == "responses" && account.IsOpenAIPassthroughEnabled() {
		switch {
		case !account.IsOpenAIApiKey():
			t.data.L01Skip = "not_apikey"
		case account.UsesOpenAICodexProtocol():
			t.data.L01Skip = "codex_protocol"
		default:
			t.data.L01Skip = "passthrough"
		}
	}
	t.data.Prefix = cachePrefixUnavailable(int(req.ContentLength))
	if model := c.GetString(OpsUpstreamModelKey); model != "" {
		t.data.Prefix.ModelGen = cachePrefixModelGeneration(model)
	}
	t.data.SessHdrOut = cacheTraceHeaderBits(req.Header)
	if req.Header.Get("Session_id") == "" {
		t.data.SessOutRelation = "none"
	}
	t.data.Transport = "http"
	s.openaiCodexTransportMu.RLock()
	pluginManager := s.pluginManager
	s.openaiCodexTransportMu.RUnlock()
	if pluginManager.ShouldRouteOpenAIOAuth(account) {
		t.data.Transport = "plugin"
	}
	// P0a does not emit WS turns; retained schema field is always zero.
	t.data.IngressWSBridge = false

	if !admitted {
		// Rejection owns only scalars on the existing trace. No job, async
		// handle, body scan, or history access is allocated on this path.
		t.data.NAReason = "budget_full"
		if chainExcluded {
			t.data.NAReason = "chain_percent"
		}
		t.skipUsage = true
		t.mu.Unlock()
		return
	}

	t.async = &cacheTraceAsync{chainExcluded: chainExcluded}
	telemetry := s.cacheTelemetry()
	telemetry.mu.Lock()
	history := telemetry.sessions[t.sessionKey].traceChains
	telemetry.mu.Unlock()
	t.pending = &cacheTraceJob{policy: t.policy, pool: pool, cost: cost, state: t.async, service: s, ingress: t.ingress, apiKeyID: t.apiKeyID, headerSource: t.headerSource,
		history: history, session: t.sessionKey, protocol: protocol, transport: t.data.Transport, getBody: req.GetBody, size: req.ContentLength, created: time.Now()}
	t.mu.Unlock()
}

func captureCacheTraceHeaders(c *gin.Context, req *http.Request) {
	t := cacheTraceFromGin(c)
	if t == nil || req == nil {
		return
	}
	t.mu.Lock()
	t.data.SessHdrOut = cacheTraceHeaderBits(req.Header)
	if req.Header.Get("Session_id") == "" {
		t.data.SessOutRelation = "none"
	}
	t.mu.Unlock()
}

// Presence is independent of billing precedence and explicit zero. It is read
// from the upstream payload before any client-facing translation.
func observeCacheTraceUsage(c *gin.Context, body []byte) {
	t := cacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		return
	}
	// Match the billing parser's fast rejection of the common delta frame.
	// Progressive usage on any event type remains observable; event type alone
	// must never suppress explicit zero or an upstream usage alias.
	if !bytes.Contains(body, []byte(`"usage"`)) && !bytes.Contains(body, []byte(`"prompt_cache_diagnostics"`)) {
		return
	}
	root := parseRawJSONView(body)
	var bits uint8
	for _, path := range []string{"usage", "response.usage", "data.usage", "data.response.usage"} {
		u := root.Get(path)
		if !u.IsObject() {
			continue
		}
		for i, paths := range [][]string{
			{"input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cached_tokens", "cache_read_tokens"},
			{"prompt_cache_hit_tokens"}, {"cache_read_input_tokens"}, {"cachedContentTokenCount"},
			{"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens"},
			{"prompt_cache_diagnostics"},
		} {
			for _, p := range paths {
				if v := u.Get(p); (i < 5 && v.Type == gjson.Number) || (i == 5 && v.Exists()) {
					bits |= 1 << i
					break
				}
			}
		}
	}
	if root.Get("prompt_cache_diagnostics").Exists() || root.Get("response.prompt_cache_diagnostics").Exists() {
		bits |= 1 << 5
	}
	t.mu.Lock()
	t.data.UsageFields |= bits
	t.mu.Unlock()
}

func (s *OpenAIGatewayService) finishCacheTrace(c *gin.Context, result *OpenAIForwardResult) {
	t := cacheTraceFromGin(c)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending.cancelPending()
	t.pending = nil
	if result == nil || !cacheTraceMessagesAttemptAllowed(c) {
		return
	}
	if t.usageObservationFailed {
		// Keep CacheTrace nil: a partial observation must never be classified.
		// This sidecar has only fixed fields and the shared billing digest.
		result.cacheTraceFailure = &cacheTraceFailure{timing: t.timing, data: cacheTraceData{
			RequestIDHMAC: s.cacheIdentityDigest("openai-request", result.RequestID),
			SessionDigest: t.data.SessionDigest, GroupID: t.data.GroupID, AccountID: t.data.AccountID,
			PromptTokens: result.Usage.InputTokens, ReadTokens: result.Usage.CacheReadInputTokens,
			PolRev: t.data.PolRev, UAFamily: t.data.UAFamily, NAReason: "observer_failed",
		}}
		return
	}
	if t.data.AccountID == 0 || t.data.ProtocolOut == "" {
		return
	}
	t.applyCacheTraceComputed()
	data := t.data
	data.PromptTokens, data.ReadTokens = result.Usage.InputTokens, result.Usage.CacheReadInputTokens
	data.RequestIDHMAC = s.cacheIdentityDigest("openai-request", result.RequestID)
	result.CacheTrace = &cacheTrace{policy: t.policy, data: data, sessionKey: t.sessionKey, chain: t.chain, async: t.async, timing: t.timing}
}

// Reuse the billing traversal's decoded frames. An independent trace pass over
// buffered SSE would repeat framing, validation and per-frame allocations.
// The disabled branch calls the unchanged parser, preserving its allocations.
func (s *OpenAIGatewayService) parseSSEUsageWithCacheTrace(c *gin.Context, body string) *OpenAIUsage {
	t := cacheTraceFromGin(c)
	if t == nil || !cacheTraceObserving(c) {
		return s.parseSSEUsageFromBody(body)
	}
	usage, complete := s.parseSSEUsageObserving(body, nil, t)
	if !complete {
		// An incomplete observation must not look like absent upstream usage.
		// Suppress this attempt's trace; billing still uses the full parser result.
		t.mu.Lock()
		t.usageObservationFailed = true
		t.mu.Unlock()
	}
	return usage
}

// The only recover is per buffered request, never per frame. Parsing has no
// settlement side effects: after an observer panic, discard its local partial
// usage and recompute with the unchanged parser. An observer error disables
// observation for remaining frames. Neither path logs the error/panic value.
func (s *OpenAIGatewayService) parseSSEUsageObserving(body string, observe func([]byte) error, indexed ...*cacheTrace) (usage *OpenAIUsage, complete bool) {
	var trace *cacheTrace
	if len(indexed) != 0 {
		trace = indexed[0]
	}
	if observe == nil && trace == nil {
		return s.parseSSEUsageFromBody(body), true
	}
	originalTrace := trace
	defer func() {
		if recover() != nil {
			usage = s.parseSSEUsageFromBody(body)
			complete = false
		}
		if !complete && originalTrace != nil {
			originalTrace.mu.Lock()
			originalTrace.usageObservationFailed = true
			originalTrace.mu.Unlock()
		}
	}()
	usage, complete = &OpenAIUsage{}, true
	forEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		if trace != nil {
			observation, fold, observed := s.parseSSEUsageBytesWithTypeIndexed(data, eventType, usage)
			if observed {
				trace.observeCacheTraceParsedResponse(observation, fold)
			}
		} else {
			s.parseSSEUsageBytesWithType(data, eventType, usage)
		}
		if observe != nil {
			if err := observe(data); err != nil {
				observe, trace, complete = nil, nil, false
			}
		}
	})
	return usage, complete
}

// startCacheTraceRequest is after the send/response-header boundary in each
// HTTP path. No scan, read, copy, or await occurs on the forwarding goroutine.
func (s *OpenAIGatewayService) startCacheTraceRequest(c *gin.Context) {
	t := cacheTraceFromGin(c)
	if t == nil {
		return
	}
	t.mu.Lock()
	job := t.pending
	t.pending = nil
	t.mu.Unlock()
	if job == nil {
		return
	}
	s.cacheTraceWorkerPool().submit(job)
}

// Requires t.mu. Only scalar computed fields are merged; source markers and
// headers remain those of the actual final forwarding attempt.
func (t *cacheTrace) applyCacheTraceComputed() {
	if t.async == nil {
		return
	}
	r := t.async.result.Load()
	t.data.NAReason = t.async.coverageReason(r)
	if r == nil {
		return
	}
	t.data.ConvAnchor = r.anchor
	t.data.PCKClassIn = r.pckIn
	t.data.IDSource = r.idSource
	t.data.ThreadPresent = t.data.ThreadPresent || r.thread
	if t.data.L01Applied {
		// The known UUID writer remains authoritative when a skipped chain
		// returns only coverage metadata without an outgoing body scan.
		t.data.PCKClassOut, t.data.PCKOutIsUUID = "gw_uuid", true
	} else if !r.pckExists {
		t.data.PCKClassOut = "none"
	} else if t.data.PCKClassOut == "unknown" {
		// Existing presence with no injection marker is client passthrough.
		t.data.PCKClassOut = "passthrough"
	}
	if !t.data.L01Applied {
		t.data.PCKOutIsUUID = r.pckUUID
	}
	modelGen := t.data.Prefix.ModelGen
	t.data.Prefix = r.prefix
	if modelGen != "unknown" && modelGen != "" {
		t.data.Prefix.ModelGen = modelGen
	}
	t.chain = r.chain
}

// Kept as a small test/helper adapter; production supplies only the numeric
// key scope to the worker, never a Gin context.
func (s *OpenAIGatewayService) cacheTraceAnchor(c *gin.Context, root gjson.Result) string {
	return s.cacheTraceAnchorForKey(getAPIKeyIDFromContext(c), root)
}

// A string-to-byte argument conversion at a call site allocates before the
// callee's guard. Keep it behind the trace gate for every streamed SSE frame.
func observeCacheTraceUsageString(c *gin.Context, payload string) {
	t := cacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		return
	}
	if !strings.Contains(payload, `"usage"`) && !strings.Contains(payload, `"prompt_cache_diagnostics"`) {
		return
	}
	observeCacheTraceUsage(c, []byte(payload))
}

// Called at response entry, outside frame traversal. Mark the observation
// attempt even when no frame contains usage: missing usage is different from
// skipped observation. The first enabled decision is sticky for this attempt,
// including buffered adapters that enter again after worker expiration. Budget
// rejection and expiration known before entry use the original billing parser.
// resetCacheTraceAttempt clears the marker before another forwarding attempt.
func cacheTraceObserving(c *gin.Context) bool {
	t := cacheTraceFromGin(c)
	if t == nil || t.skipUsage || !cacheTraceMessagesAttemptAllowed(c) {
		return false
	}
	t.mu.Lock()
	if !t.data.UsagePromptObserved && t.async != nil && t.async.reason.Load() != 0 {
		t.mu.Unlock()
		return false
	}
	t.data.UsagePromptObserved = true
	t.mu.Unlock()
	return true
}

// The trace snapshot already owns the same HMAC needed by both legacy usage
// events. Disabled requests retain the original single calculation per event.
func (s *OpenAIGatewayService) cacheUsageRequestDigest(result *OpenAIForwardResult) string {
	if result.CacheTrace != nil && result.CacheTrace.data.RequestIDHMAC != "" {
		return result.CacheTrace.data.RequestIDHMAC
	}
	if result.cacheTraceFailure != nil {
		return result.cacheTraceFailure.data.RequestIDHMAC
	}
	return s.cacheIdentityDigest("openai-request", result.RequestID)
}

// The existing cohort algorithm is preserved. Existing session digests fit
// 64 bytes, so an explicit stack array avoids string-to-slice allocation.
// Cache this once on the trace; rejected attempts only compare an integer.
func cacheTraceSessionCohort(session string) int {
	if len(session) > 64 {
		return sha256SumCacheCohort(session) // defensive non-production adapter
	}
	var raw [64]byte
	copy(raw[:], session)
	digest := sha256.Sum256(raw[:len(session)])
	return int(digest[0]) * 100 / 256
}
