package service

import (
	"crypto/sha256"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	openAIUpstreamIdentityCohortGinKey = "openaiL01Cohort"
	openAIUpstreamIdentityWireGinKey   = "openaiL01Wire"
)

// The cohort belongs to the authenticated request. The UUID belongs to its
// serving-scoped routing identity, never to an account, host or billing origin.
// Only the forwarding layer consumes this policy; no scheduling state changes.
type openAIUpstreamIdentityDecision struct {
	Arm, Skip      string
	PolicyRevision int64
	RoutingSession string
	ChatCompatUUID bool
}

func (d openAIUpstreamIdentityDecision) eligible() bool {
	return d.Arm == "on" && d.Skip == "none"
}

func prepareOpenAIUpstreamIdentity(c *gin.Context, account *Account) openAIUpstreamIdentityDecision {
	p := CachePolicyFromGin(c).UpstreamIdentity()
	d := openAIUpstreamIdentityDecision{Arm: "none", Skip: "policy_off", PolicyRevision: p.Revision(), ChatCompatUUID: p.ChatCompatUUID}
	// Before any body read, UUID derivation, context write or allocation.
	if !p.Enabled() || c == nil || c.Request == nil || c.Request.URL == nil {
		return d
	}
	// A previous attempt's generated header must not follow an ineligible account.
	if _, exists := c.Get(openAIUpstreamIdentityWireGinKey); exists {
		c.Set(openAIUpstreamIdentityWireGinKey, nil)
	}
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS || (!strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/responses/compact") && !strings.HasSuffix(path, "/chat/completions")) {
		d.Skip = "unsupported_ingress"
		return d
	}
	switch {
	case account == nil || !account.IsOpenAIApiKey():
		d.Skip = "not_apikey"
		return d
	case account.UsesOpenAICodexProtocol():
		d.Skip = "codex_protocol"
		return d
	case account.IsOpenAIPassthroughEnabled():
		d.Skip = "passthrough"
		return d
	case account.Platform == PlatformGrok || isGrokRequestContext(c):
		d.Skip = "grok"
		return d
	case account.GetCodexFingerprintMode() != codexFingerprintOff:
		d.Skip = "fingerprint_converged"
		return d
	case !p.AllowsAccount(account.ID):
		d.Skip = "account_out_of_scope"
		return d
	case monitorProbeRequest(c):
		d.Skip = "monitor_probe"
		return d
	}
	session, _ := c.Get(openAICacheSessionGinKey)
	d.RoutingSession, _ = session.(string)
	if d.RoutingSession == "" {
		d.Skip = "no_routing_session"
		return d
	}
	treatment, fixed := cachePolicyL01CohortFromGin(c)
	if !fixed {
		treatment = openAIUpstreamIdentityCohort(d.RoutingSession) < p.RolloutPercent
		setCachePolicyL01Cohort(c, CachePolicyFromGin(c), treatment)
	}
	if !treatment {
		d.Arm, d.Skip = "off", "arm_off"
		return d
	}
	d.Arm = "on"
	d.Skip = "none"
	return d
}

// Routing digests are 16 hex characters, optionally prefixed with v2:.
// Keep their SHA-256 input on the stack: converting a dynamic string longer
// than the compiler's small buffer allocates. The calculation is identical to
// sha256SumCacheCohort; unexpected longer identities keep its exact behavior.
func openAIUpstreamIdentityCohort(session string) int {
	var input [64]byte
	if len(session) > len(input) {
		return sha256SumCacheCohort(session)
	}
	n := copy(input[:], session)
	digest := sha256.Sum256(input[:n])
	return int(digest[0]) * 100 / 256
}

// Canonicalize fixed header keys once; doing this per request allocates for
// uncommon names, even when a cohort or client field excludes the request.
var openAIUpstreamIdentityHeaderKeys = func() []string {
	keys := make([]string, len(explicitOpenAIHeaderSessionNames))
	for i, name := range explicitOpenAIHeaderSessionNames {
		keys[i] = http.CanonicalHeaderKey(name)
	}
	return keys
}()

func openAIUpstreamIdentityHasClientHeader(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	for _, name := range openAIUpstreamIdentityHeaderKeys {
		if _, exists := c.Request.Header[name]; exists {
			return true
		}
	}
	return false
}

// Use only after existing request parsing established field absence. The JSON
// suffix is copied verbatim; this never reparses input/tools, applies patches,
// calls GenerateSessionHash, or changes the immutable lineage entry body.
func fillOpenAIUpstreamIdentityBody(body []byte, hasPromptCacheKey bool, uuid string) []byte {
	if hasPromptCacheKey || uuid == "" {
		return body
	}
	i := 0
	for i < len(body) && (body[i] == ' ' || body[i] == '\n' || body[i] == '\r' || body[i] == '\t') {
		i++
	}
	if i == len(body) || body[i] != '{' {
		return body
	}
	j := i + 1
	for j < len(body) && (body[j] == ' ' || body[j] == '\n' || body[j] == '\r' || body[j] == '\t') {
		j++
	}
	if j == len(body) {
		return body
	}
	const prefix = `"prompt_cache_key":"`
	n := len(body) + len(prefix) + len(uuid) + 1
	if body[j] != '}' {
		n++
	}
	out := make([]byte, 0, n)
	out = append(out, body[:i+1]...)
	out = append(out, prefix...)
	out = append(out, uuid...)
	out = append(out, '"')
	if body[j] != '}' {
		out = append(out, ',')
	}
	return append(out, body[i+1:]...)
}

type openAIUpstreamIdentityWire struct {
	accountID int64
	uuid      string
}

func rememberOpenAIUpstreamIdentityWire(c *gin.Context, account *Account, uuid string) {
	c.Set(openAIUpstreamIdentityWireGinKey, openAIUpstreamIdentityWire{accountID: account.ID, uuid: uuid})
}

func applyUpstreamSessionIdentity(req *http.Request, c *gin.Context, uuid string) {
	if req == nil || uuid == "" {
		return
	}
	if _, exists := req.Header[http.CanonicalHeaderKey("session_id")]; exists {
		return
	}
	req.Header.Set("session_id", uuid)
	cacheTraceFromGin(c).setSessionSource("eq_pck")
}

func applyOpenAIUpstreamIdentityHeaders(c *gin.Context, account *Account, req *http.Request) {
	if !CachePolicyFromGin(c).UpstreamIdentity().Enabled() {
		return
	}
	value, _ := c.Get(openAIUpstreamIdentityWireGinKey)
	wire, ok := value.(openAIUpstreamIdentityWire)
	if !ok || account == nil || wire.accountID != account.ID {
		return
	}
	applyUpstreamSessionIdentity(req, c, wire.uuid)
}

// Only fixed enums and the policy revision enter the internal event. UUIDs,
// request headers and the routing seed are deliberately absent from trace data.
func recordOpenAIUpstreamIdentity(c *gin.Context, d openAIUpstreamIdentityDecision, applied bool, skip string) {
	t := cacheTraceFromGin(c)
	if t == nil {
		return
	}
	t.mu.Lock()
	t.data.L01Arm, t.data.L01Skip = d.Arm, skip
	t.data.L01Applied, t.data.L01PolicyRevision = applied, d.PolicyRevision
	if applied {
		// The writer generated a UUID; this fact does not require the worker.
		t.data.PCKOutIsUUID = true
	}
	t.mu.Unlock()
}
