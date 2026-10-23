package service

import (
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

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type cacheTraceKey struct{}

// The trace is request-local and contains only enums, counts and digests.
// A separate snapshot is attached to the forward result before async billing.
// It never owns request/response bodies, headers, or a Gin context.
type cacheTrace struct {
	mu         sync.Mutex
	data       cacheTraceData
	sessionKey string
	emitted    bool
}

type cacheTraceData struct {
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
	for i, name := range cacheTraceHeaders {
		if _, ok := h[http.CanonicalHeaderKey(name)]; ok {
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

func cacheTraceProtocol(c *gin.Context, root gjson.Result) string {
	if c != nil && c.Request != nil && strings.Contains(c.Request.URL.Path, "/chat/completions") {
		return "chat"
	}
	if root.Get("messages").IsArray() {
		return "chat"
	}
	return "responses"
}

func traceDigestPrefix(value string, n int) string {
	if len(value) > n {
		return value[:n]
	}
	return value
}

// Each part is length-delimited; no concatenation ambiguity or retained content.
func (s *OpenAIGatewayService) cacheTraceAnchor(c *gin.Context, root gjson.Result) string {
	h := hmac.New(sha256.New, []byte(s.cfg.JWT.Secret))
	_, _ = io.WriteString(h, "cache-trace-anchor-v1\x00"+strconv.FormatInt(getAPIKeyIDFromContext(c), 10))
	var size [8]byte
	part := func(raw string) {
		binary.BigEndian.PutUint64(size[:], uint64(len(raw)))
		_, _ = h.Write(size[:])
		_, _ = io.WriteString(h, raw)
	}
	part(root.Get("instructions").Raw)
	items := root.Get("input")
	if !items.Exists() {
		items = root.Get("messages")
	}
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
	if !s.cacheTraceEnabled() || c == nil || c.Request == nil || getAPIKeyIDFromContext(c) <= 0 || isGrokRequestContext(c) || monitorProbeRequest(c) {
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
		t.data.SessionDigest = traceDigestPrefix(s.cacheIdentityDigest("cache-trace-session", sessionKey), 24)
		t.mu.Unlock()
		return
	}
	root := parseRawJSONView(body)
	source := "content_seed"
	if explicitOpenAIHeaderSessionID(c) != "" {
		source = "header"
	} else if strings.TrimSpace(root.Get("prompt_cache_key").String()) != "" {
		source = "body_pck"
	}
	pck := cacheTracePCKIn(root.Get("prompt_cache_key"))
	out := "none"
	if pck != "none" {
		out = "passthrough"
	}
	bits := cacheTraceHeaderBits(c.Request.Header)
	t := &cacheTrace{sessionKey: sessionKey, data: cacheTraceData{
		SessionDigest: traceDigestPrefix(s.cacheIdentityDigest("cache-trace-session", sessionKey), 24),
		ConvAnchor:    s.cacheTraceAnchor(c, root), GroupID: getOpenAIGroupIDFromContext(c),
		ProtocolIn: cacheTraceProtocol(c, root), PCKClassIn: pck, PCKClassOut: out,
		SessHdrIn: bits, SessOutRelation: "none", IDSource: source,
		ThreadPresent: bits&((1<<7)|(1<<8)) != 0 || root.Get("client_metadata.thread_id").Exists(),
		Transport:     "http", RouteReason: "unknown", RebindCause: "none",
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
	case "none", "client_value", "uuid_of_pck", "gw_isolated":
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
	t.data.PCKClassOut = "none"
	if t.data.PCKClassIn != "none" {
		t.data.PCKClassOut = "passthrough"
	}
	t.data.PCKOutIsUUID = false
	t.data.SessHdrOut = 0
	t.data.SessOutRelation = "none"
	t.data.UsageFields = 0
	t.data.AccountID = 0
	t.data.ProtocolOut = ""
	t.data.Transport = "http"
}

func (s *OpenAIGatewayService) captureCacheTraceRequest(c *gin.Context, req *http.Request, account *Account, protocol string) {
	t := cacheTraceFromGin(c)
	if t == nil || account == nil || account.Platform != PlatformOpenAI || req == nil || req.GetBody == nil {
		return
	}
	r, err := req.GetBody()
	if err != nil {
		return
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return
	}
	root := parseRawJSONView(body)
	t.mu.Lock()
	t.data.AccountID = account.ID
	t.data.ProtocolOut = protocol
	pck := root.Get("prompt_cache_key")
	if !pck.Exists() {
		t.data.PCKClassOut = "none"
	}
	t.data.PCKOutIsUUID = pck.Type == gjson.String && cacheTraceUUID.MatchString(pck.Str)
	t.data.SessHdrOut = cacheTraceHeaderBits(req.Header)
	if req.Header.Get("session_id") == "" {
		t.data.SessOutRelation = "none"
	}
	t.data.Transport = "http"
	if s.pluginManager.ShouldRouteOpenAIOAuth(account) {
		t.data.Transport = "plugin"
	}
	// HTTP builders called for a WS client are the host HTTP bridge. Native WS
	// sends bypass these builders; no plugin code or turn state is inspected.
	t.data.IngressWSBridge = GetOpenAIClientTransport(c) == OpenAIClientTransportWS
	t.mu.Unlock()
}

func captureCacheTraceHeaders(c *gin.Context, req *http.Request) {
	t := cacheTraceFromGin(c)
	if t == nil || req == nil {
		return
	}
	t.mu.Lock()
	t.data.SessHdrOut = cacheTraceHeaderBits(req.Header)
	if req.Header.Get("session_id") == "" {
		t.data.SessOutRelation = "none"
	}
	t.mu.Unlock()
}

// Presence is independent of billing precedence and explicit zero. It is read
// from the upstream payload before any client-facing translation.
func observeCacheTraceUsage(c *gin.Context, body []byte) {
	t := cacheTraceFromGin(c)
	if t == nil {
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
				if u.Get(p).Exists() {
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
	if t == nil || result == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data.AccountID == 0 || t.data.ProtocolOut == "" {
		return
	}
	data := t.data
	data.RequestIDHMAC = traceDigestPrefix(s.cacheIdentityDigest("openai-request", result.RequestID), 24)
	result.CacheTrace = &cacheTrace{data: data, sessionKey: t.sessionKey}
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
	d := t.data
	slog.Info("cache_trace", "v", 1, "request_id_hmac", d.RequestIDHMAC, "session_digest", d.SessionDigest,
		"platform", "openai", "protocol_in", d.ProtocolIn, "protocol_out", d.ProtocolOut,
		"group_id", d.GroupID, "account_id", d.AccountID, "conv_anchor", d.ConvAnchor,
		"pck_class_in", d.PCKClassIn, "pck_class_out", d.PCKClassOut, "pck_out_is_uuid", traceBool(d.PCKOutIsUUID),
		"sess_hdr_in", int(d.SessHdrIn), "sess_hdr_out", int(d.SessHdrOut), "sess_out_relation", d.SessOutRelation,
		"id_source", d.IDSource, "thread_present", traceBool(d.ThreadPresent), "usage_fields", int(d.UsageFields),
		"transport", d.Transport, "ingress_ws_bridge", traceBool(d.IngressWSBridge), "route_reason", d.RouteReason, "rebind_cause", d.RebindCause)
}

func observeCacheTraceSSE(c *gin.Context, body []byte) {
	if cacheTraceFromGin(c) == nil {
		return
	}
	forEachOpenAISSEFrame(string(body), func(_ string, data []byte) { observeCacheTraceUsage(c, data) })
}
