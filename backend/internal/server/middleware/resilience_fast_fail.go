package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

// resilienceFastFailEligible reports whether a request takes part in the
// per-key fast fail (design §9.3). Reads, polling, DELETE, capability
// discovery, usage/models and idempotent replays are exempt.
func resilienceFastFailEligible(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Method != http.MethodPost {
		return false
	}
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) != "" {
		return false
	}
	path := c.Request.URL.Path
	for _, exempt := range []string{"/usage", "/models", "/capabilities", "/sub2api/billing"} {
		if strings.HasSuffix(path, exempt) {
			return false
		}
	}
	return true
}

// resilienceFastFailReplay answers a request of a tripped key with the same
// response before account selection and moderation. It reports whether the
// request was answered.
func resilienceFastFailReplay(c *gin.Context, apiKeyID int64) bool {
	cfg := resilience.Current()
	if !cfg.ErrorsOn() || !cfg.Errors.FastFail.Enabled || !resilienceFastFailEligible(c) {
		return false
	}
	now := time.Now()
	resp, tripped := resilience.ProcessFastFail().Check(apiKeyID, now)
	if !tripped || resp.Status == 0 || len(resp.Body) == 0 {
		return false
	}
	// The 503s were for one model; the key's requests for other models are
	// served normally. The body is read only on this rare tripped path.
	if resp.Model != "" && requestedModel(c) != resp.Model {
		return false
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			c.Writer.Header().Add(k, v)
		}
	}
	if secs := resp.RetryAfterSeconds(now); secs > 0 {
		c.Header("Retry-After", strconv.Itoa(secs))
	}
	c.Header("X-XY-Fast-Fail", "1")
	resilience.Count(resilience.MetricFastFail, resilience.StateFrom(c.Request.Context()).GroupID(), 0, strconv.Itoa(resp.Status))
	c.Data(resp.Status, "application/json", resp.Body)
	c.Abort()
	return true
}

// requestedModel reads the model of a request before any handler parsed
// it: from the Gemini path (/models/{model}:method) or the JSON body, which
// is restored for the handler. It returns "" when neither names one.
func requestedModel(c *gin.Context) string {
	path := c.Request.URL.Path
	if i := strings.Index(path, "/models/"); i >= 0 {
		m := path[i+len("/models/"):]
		if j := strings.IndexAny(m, ":/"); j >= 0 {
			m = m[:j]
		}
		return m
	}
	if c.Request.Body == nil || !strings.Contains(strings.ToLower(c.ContentType()), "json") {
		return ""
	}
	head, err := io.ReadAll(io.LimitReader(c.Request.Body, requestedModelPeekLimit))
	c.Request.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), c.Request.Body), c.Request.Body}
	if err != nil {
		return ""
	}
	return gjson.GetBytes(head, "model").String()
}

// requestedModelPeekLimit bounds the body read before the handler; a model
// field after it is not found and the request is not replayed.
const requestedModelPeekLimit = 4 << 20

// resilienceFastFailObserve records the outcome of a request after the
// handler ran: identical 503s count toward the trip, any success clears it.
func resilienceFastFailObserve(c *gin.Context, apiKeyID int64) {
	cfg := resilience.Current()
	if !cfg.ErrorsOn() || !cfg.Errors.FastFail.Enabled || !resilienceFastFailEligible(c) {
		return
	}
	status := c.Writer.Status()
	if status >= 200 && status < 300 {
		resilience.ProcessFastFail().Reset(apiKeyID)
		return
	}
	if status != http.StatusServiceUnavailable || c.GetBool(resilience.GinKeyTurnCapped) {
		// A capped turn's final answer carries no Retry-After; replaying it
		// with one would restart the client's retries (design §9.7).
		return
	}
	// Only answers that hold for every request of the key and model: a
	// group-level no-account result or a configuration error. A 503 that
	// concerned one session must not stop the key's other sessions.
	if st := resilience.StateFrom(c.Request.Context()); !st.GroupNoAccount() && !st.ConfigError() {
		return
	}
	v, ok := c.Get(resilience.GinKeyErrorResponse)
	if !ok {
		return
	}
	er, ok := v.(resilience.ErrorResponse)
	if !ok || er.Status != http.StatusServiceUnavailable {
		return
	}
	now := time.Now()
	header := http.Header{}
	if s := c.Writer.Header().Get("x-should-retry"); s != "" {
		header.Set("x-should-retry", s)
	}
	retryAt := now.Add(time.Duration(cfg.Errors.FastFail.WindowSeconds) * time.Second)
	if secs, err := strconv.Atoi(c.Writer.Header().Get("Retry-After")); err == nil && secs > 0 {
		retryAt = now.Add(time.Duration(secs) * time.Second)
	}
	tripped := resilience.ProcessFastFail().Observe(apiKeyID,
		resilience.Signature(er.Status, er.ErrType, er.Message),
		resilience.CachedResponse{Status: er.Status, Body: er.Body, Header: header, RetryAt: retryAt,
			Model: resilience.StateFrom(c.Request.Context()).Model()},
		time.Duration(cfg.Errors.FastFail.WindowSeconds)*time.Second, cfg.Errors.FastFail.Threshold, now)
	if tripped {
		slog.Warn("resilience.fast_fail", "api_key_id", apiKeyID, "status", er.Status, "error_type", er.ErrType, "retry_at", retryAt)
	}
}

// resilienceTurnCapObserve ends a conversation's failed-turn streak (design
// §9.7) when a request succeeded; a 200 that carried an in-stream failure
// counted as a failure and keeps it.
func resilienceTurnCapObserve(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if status := c.Writer.Status(); status < 200 || status >= 300 {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	if st.TurnFailed() {
		return
	}
	resilience.ProcessTurnCap().Reset(st.TurnKey())
}

// resilienceNoteGroup records the API key's group on the request state so
// failover can find accounts sharing a relay host (design §6.3).
func resilienceNoteGroup(c *gin.Context, apiKey *service.APIKey) {
	if c == nil || c.Request == nil || apiKey == nil {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	st.SetAPIKey(apiKey.ID)
	if apiKey.GroupID != nil {
		st.SetGroup(*apiKey.GroupID)
	}
}

// resilienceNoteEdge records the CDN edge of the request (design §7.1). An
// X-XY-Edge marker set by the reverse proxy counts only when the TCP peer is
// one of the configured trusted proxies; anything else could be forged by
// the client. Without a valid marker the edge comes from the headers the
// CDN adds to origin requests, so CDN deployments need no proxy setup.
func resilienceNoteEdge(c *gin.Context, cfg *config.Config) {
	if c == nil || c.Request == nil {
		return
	}
	allowed := resilience.Current().Keepalive.Edges
	edge := ""
	if raw := c.GetHeader(resilience.EdgeHeader); raw != "" {
		trusted := cfg != nil && cfg.Server.TrustedProxiesConfigured && peerIsTrustedProxy(c.RemoteIP(), cfg.Server.TrustedProxies)
		edge = resilience.EdgeOf(raw, trusted, allowed)
	}
	if edge == "" {
		edge = resilience.EdgeFromCDNHeaders(c.Request.Header, allowed)
	}
	if edge != "" {
		resilience.StateFrom(c.Request.Context()).SetEdge(edge)
	}
}

type trustedProxySet struct {
	src      []string
	prefixes []netip.Prefix
}

var trustedProxyCache atomic.Pointer[trustedProxySet]

// peerIsTrustedProxy reports whether peer belongs to the trusted proxy list
// (IPs or CIDRs). The parsed list is cached for the static configuration.
func peerIsTrustedProxy(peer string, proxies []string) bool {
	if len(proxies) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(peer))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	set := trustedProxyCache.Load()
	if set == nil || len(set.src) != len(proxies) || &set.src[0] != &proxies[0] {
		set = &trustedProxySet{src: proxies}
		for _, entry := range proxies {
			entry = strings.TrimSpace(entry)
			if prefix, err := netip.ParsePrefix(entry); err == nil {
				set.prefixes = append(set.prefixes, prefix.Masked())
				continue
			}
			if ip, err := netip.ParseAddr(entry); err == nil {
				ip = ip.Unmap()
				set.prefixes = append(set.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
			}
		}
		trustedProxyCache.Store(set)
	}
	for _, prefix := range set.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
