package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
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
	mu                     sync.Mutex
	data                   cacheTraceData
	sessionKey             string
	emitted                bool
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
	LossClass, NAReason                      string
	PromptTokens, ReadTokens                 int
	Prefix                                   cachePrefixObservation
	RequestIDHMAC, SessionDigest, ConvAnchor string
	ProtocolIn, ProtocolOut                  string
	GroupID, AccountID                       int64
	PCKClassIn, PCKClassOut                  string
	PCKOutIsUUID                             bool
	SessHdrIn, SessHdrOut                    uint16
	SessOutRelation, IDSource                string
	ThreadPresent                            bool
	UsageFields                              uint8
	Transport                                string
	IngressWSBridge                          bool
	RouteReason, RebindCause                 string
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

func (s *OpenAIGatewayService) cacheTraceEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.CacheTrace.Enabled && s.cfg.Gateway.OpenAICache.DiagnosticsEnabled && s.cfg.JWT.Secret != ""
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
	if !s.cacheTraceEnabled() || c == nil || c.Request == nil || c.Request.URL == nil || getAPIKeyIDFromContext(c) <= 0 || isGrokRequestContext(c) || monitorProbeRequest(c) {
		return
	}
	// P0a covers HTTP Responses (including compact) and Chat only. Messages,
	// WS turns, count_tokens, images and search need their own P0b metadata.
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS || (!strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/responses/compact") && !strings.HasSuffix(path, "/chat/completions")) {
		return
	}
	if key, ok := c.Get("api_key"); ok {
		if api, ok := key.(*APIKey); ok && api.Group != nil && api.Group.Platform != "" && api.Group.Platform != PlatformOpenAI {
			return
		}
	}
	session, _ := c.Get(openAICacheSessionGinKey)
	sessionKey, _ := session.(string)
	if sessionKey == "" {
		return
	}
	if t := cacheTraceFromGin(c); t != nil {
		t.mu.Lock()
		t.sessionKey = sessionKey
		t.data.GroupID = getOpenAIGroupIDFromContext(c)
		t.data.SessionDigest = sessionKey
		t.mu.Unlock()
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
	}
	t := &cacheTrace{sessionKey: sessionKey, ingress: body, apiKeyID: getAPIKeyIDFromContext(c), headerSource: headerSource, data: cacheTraceData{
		SessionDigest: sessionKey, GroupID: getOpenAIGroupIDFromContext(c),
		ProtocolIn: protocol, PCKClassIn: "unknown", PCKClassOut: "unknown",
		SessHdrIn: bits, SessOutRelation: "none", IDSource: source,
		ThreadPresent: bits&((1<<7)|(1<<8)) != 0,
		Transport:     "http", RouteReason: "unknown", RebindCause: "none", Prefix: cachePrefixUnavailable(0),
	}}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, t))
}

func (t *cacheTrace) setPCKSource(source string) {
	if t == nil {
		return
	}
	switch source {
	case "none", "passthrough", "gw_compat_hex", "gw_from_header", "gw_grok", "oauth_rewritten":
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
	case "none", "client_value", "uuid_of_pck", "gw_isolated", "fingerprint_converged":
	default:
		return
	}
	t.mu.Lock()
	t.data.SessOutRelation = source
	t.mu.Unlock()
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
	if t == nil || account == nil || account.Platform != PlatformOpenAI || req == nil || req.GetBody == nil {
		return
	}

	t.mu.Lock()
	// Free an unsent/replaced attempt before trying to own the final wire body.
	t.pending.cancelPending()
	t.pending = nil
	t.skipUsage = false
	pool := s.cacheTraceWorkerPool()
	cost, admitted := pool.reserve(int64(len(t.ingress)), req.ContentLength)

	t.data.AccountID = account.ID
	t.data.ProtocolOut = protocol
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
		// A fixed-size state keeps the settlement snapshot independent of retries.
		// In particular, rejected traffic never allocates a job or visits history.
		t.async = &cacheTraceAsync{}
		t.async.reason.Store(cacheTraceBudgetFull)
		t.skipUsage = true
		t.mu.Unlock()
		return
	}

	t.async = &cacheTraceAsync{chainExcluded: sha256SumCacheCohort(t.sessionKey) >= s.cfg.Gateway.CacheTrace.ChainPercent}
	telemetry := s.cacheTelemetry()
	telemetry.mu.Lock()
	history := telemetry.sessions[t.sessionKey].traceChains
	telemetry.mu.Unlock()
	t.pending = &cacheTraceJob{pool: pool, cost: cost, state: t.async, service: s, ingress: t.ingress, apiKeyID: t.apiKeyID, headerSource: t.headerSource,
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
	if result == nil {
		return
	}
	if t.usageObservationFailed {
		return
	}
	if t.data.AccountID == 0 || t.data.ProtocolOut == "" {
		return
	}
	t.applyCacheTraceComputed()
	data := t.data
	data.PromptTokens, data.ReadTokens = result.Usage.InputTokens, result.Usage.CacheReadInputTokens
	data.RequestIDHMAC = s.cacheIdentityDigest("openai-request", result.RequestID)
	result.CacheTrace = &cacheTrace{data: data, sessionKey: t.sessionKey, chain: t.chain, async: t.async}
}

func traceBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (t *cacheTrace) emitCacheTrace() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.emitted {
		return
	}
	t.emitted = true
	t.applyCacheTraceComputed()
	d := t.data
	// Coverage gaps retain the strict schema's identifiers and billing totals.
	// No body-derived metadata, logger.With, or large event is built here.
	if d.Transport != "plugin" && (d.LossClass == "" || d.LossClass == "n/a_sampled_out") &&
		(d.NAReason == "budget_full" || d.NAReason == "expired" || d.NAReason == "not_ready") {
		slog.LogAttrs(context.Background(), slog.LevelInfo, "cache_trace",
			slog.Int("v", 1), slog.String("platform", "openai"),
			slog.String("loss_class", "n/a_sampled_out"), slog.String("na_reason", d.NAReason),
			slog.String("request_id_hmac", d.RequestIDHMAC), slog.String("session_digest", d.SessionDigest),
			slog.Int64("account_id", d.AccountID), slog.Int64("group_id", d.GroupID),
			slog.Int("P", d.PromptTokens), slog.Int("read", d.ReadTokens))
		return
	}
	logger := slog.Default()
	if d.LossClass != "" {
		logger = logger.With("loss_class", d.LossClass)
		if d.LossClass == "n/a_sampled_out" {
			logger = logger.With("na_reason", d.NAReason)
		}
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "cache_trace",
		slog.Int("v", 1),
		slog.String("request_id_hmac", d.RequestIDHMAC),
		slog.String("session_digest", d.SessionDigest),
		slog.String("platform", "openai"),
		slog.String("protocol_in", d.ProtocolIn),
		slog.String("protocol_out", d.ProtocolOut),
		slog.Int64("group_id", d.GroupID),
		slog.Int64("account_id", d.AccountID),
		slog.String("conv_anchor", d.ConvAnchor),
		slog.String("pck_class_in", d.PCKClassIn),
		slog.String("pck_class_out", d.PCKClassOut),
		slog.Int("pck_out_is_uuid", traceBool(d.PCKOutIsUUID)),
		slog.Int("sess_hdr_in", int(d.SessHdrIn)),
		slog.Int("sess_hdr_out", int(d.SessHdrOut)),
		slog.String("sess_out_relation", d.SessOutRelation),
		slog.String("id_source", d.IDSource),
		slog.Int("thread_present", traceBool(d.ThreadPresent)),
		slog.Int("usage_fields", int(d.UsageFields)),
		slog.Int("blk_n", d.Prefix.BlockN),
		slog.Int("bytes_out", d.Prefix.BytesOut),
		slog.Int("fork_blk", d.Prefix.ForkBlock),
		slog.Int("fork_byte", d.Prefix.ForkByte),
		slog.String("fork_ref", d.Prefix.ForkRef),
		slog.String("fork_kind", d.Prefix.ForkKind),
		slog.String("fork_precision", d.Prefix.ForkPrecision),
		slog.Int("fork_blk_hi", d.Prefix.ForkBlockHi),
		slog.Int("fork_byte_hi", d.Prefix.ForkByteHi),
		slog.Int("p_prev", d.Prefix.PreviousPrompt),
		slog.Int("hist_age_s", d.Prefix.HistoryAgeSeconds),
		slog.Int("chain_sampled", traceBool(d.Prefix.Sampled)),
		slog.Int("bytes_out_raw", d.Prefix.BytesOutRaw),
		slog.Int("fork_raw_byte", d.Prefix.ForkRawByte),
		slog.Int("first_old_reasoning_raw_byte", d.Prefix.FirstOldReasoningRawByte),
		slog.Int("blk_order_physical_eq_logical", traceBool(d.Prefix.BlockOrderPhysicalEqLogical)),
		slog.String("tail_kind", d.Prefix.TailKind),
		slog.Int("reasoning_items_n", d.Prefix.ReasoningItemsN),
		slog.Int("old_reasoning_n", d.Prefix.OldReasoningN),
		slog.Int("first_old_reasoning_blk", d.Prefix.FirstOldReasoningBlock),
		slog.Int("first_old_reasoning_byte", d.Prefix.FirstOldReasoningByte),
		slog.Int("include_encrypted_reasoning", traceBool(d.Prefix.IncludeEncryptedReasoning)),
		slog.Int("store_false", traceBool(d.Prefix.StoreFalse)),
		slog.Int("reasoning_enc_n", d.Prefix.ReasoningEncryptedN),
		slog.String("reasoning_ctx", d.Prefix.ReasoningContext),
		slog.String("model_gen", d.Prefix.ModelGen),
		slog.String("transport", d.Transport),
		slog.Int("ingress_ws_bridge", traceBool(d.IngressWSBridge)),
		slog.String("route_reason", d.RouteReason),
		slog.String("rebind_cause", d.RebindCause),
	)
}

// Reuse the billing traversal's decoded frames. An independent trace pass over
// buffered SSE would repeat framing, validation and per-frame allocations.
// The disabled branch calls the unchanged parser, preserving its allocations.
func (s *OpenAIGatewayService) parseSSEUsageWithCacheTrace(c *gin.Context, body string) *OpenAIUsage {
	t := cacheTraceFromGin(c)
	if t == nil || !cacheTraceObserving(c) {
		return s.parseSSEUsageFromBody(body)
	}
	usage, complete := s.parseSSEUsageObserving(body, func(data []byte) error {
		observeCacheTraceUsage(c, data)
		return nil
	})
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
func (s *OpenAIGatewayService) parseSSEUsageObserving(body string, observe func([]byte) error) (usage *OpenAIUsage, complete bool) {
	if observe == nil {
		return s.parseSSEUsageFromBody(body), true
	}
	defer func() {
		if recover() != nil {
			usage = s.parseSSEUsageFromBody(body)
			complete = false
		}
	}()
	usage, complete = &OpenAIUsage{}, true
	forEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		s.parseSSEUsageBytesWithType(data, eventType, usage)
		if observe != nil {
			if err := observe(data); err != nil {
				observe, complete = nil, false
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
	if !r.pckExists {
		t.data.PCKClassOut = "none"
	} else if t.data.PCKClassOut == "unknown" {
		// Existing presence with no injection marker is client passthrough.
		t.data.PCKClassOut = "passthrough"
	}
	t.data.PCKOutIsUUID = r.pckUUID
	modelGen := t.data.Prefix.ModelGen
	t.data.Prefix = r.prefix
	if modelGen != "n/a" && modelGen != "" {
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

// Called once at response entry, outside frame traversal. The budget-full fast
// path uses the original billing parser; expiration already known at response
// entry does likewise. Workers never mutate the request-local trace itself.
func cacheTraceObserving(c *gin.Context) bool {
	t := cacheTraceFromGin(c)
	if t == nil || t.skipUsage {
		return false
	}
	return t.async == nil || t.async.reason.Load() == 0
}

// The trace snapshot already owns the same HMAC needed by both legacy usage
// events. Disabled requests retain the original single calculation per event.
func (s *OpenAIGatewayService) cacheUsageRequestDigest(result *OpenAIForwardResult) string {
	if result.CacheTrace != nil && result.CacheTrace.data.RequestIDHMAC != "" {
		return result.CacheTrace.data.RequestIDHMAC
	}
	return s.cacheIdentityDigest("openai-request", result.RequestID)
}
