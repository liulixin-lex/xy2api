package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// ginKeyPreOutputKeepaliveStop holds the stop function of the request's
// pre-output heartbeat.
const ginKeyPreOutputKeepaliveStop = "xy_resilience_pre_output_keepalive_stop"

// armPreOutputKeepalive starts the delayed pre-output heartbeat before an
// upstream attempt when the request qualifies (design §7.2): a streaming
// text request that arrived through a CDN edge, after authentication,
// billing checks and selection have passed, on an account that is not an
// official OAuth account. The heartbeat clock runs from the request start
// and is not reset by account switches; later calls are no-ops, except that
// an uncommitted heartbeat stops when an official OAuth account takes over.
func armPreOutputKeepalive(c *gin.Context, protocol string, stream bool, account *service.Account) {
	if c == nil || c.Request == nil {
		return
	}
	// The attempt is about to wait for upstream headers (design §8.2 stages).
	resilience.StateFrom(c.Request.Context()).SetStage(resilience.StageHeaderWait)
	if !stream {
		return
	}
	cfg := resilience.Current()
	if !cfg.KeepaliveOn() {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	if st.Edge() == "" {
		return
	}
	b := st.Budget()
	if b != nil && b.Kind() != resilience.KindText {
		return
	}
	if cfg.Keepalive.SkipOfficialOAuth && isOfficialOAuthAccount(account) {
		service.StopPreOutputKeepaliveIfUncommitted(c)
		return
	}
	if _, armed := c.Get(ginKeyPreOutputKeepaliveStop); armed {
		return
	}
	start := time.Now()
	if b != nil && !b.Started().IsZero() {
		start = b.Started()
	}
	delay := time.Duration(cfg.Keepalive.DelaySeconds)*time.Second - time.Since(start)
	interval := time.Duration(cfg.Keepalive.IntervalSeconds) * time.Second
	c.Set(ginKeyPreOutputKeepaliveStop, service.StartPreOutputKeepalive(c, protocol, delay, interval))
}

// stopPreOutputKeepalive stops the request's pre-output heartbeat and
// restores the original response writer. It is meant to be deferred by the
// handler that arms the heartbeat.
func stopPreOutputKeepalive(c *gin.Context) {
	if c == nil {
		return
	}
	if v, ok := c.Get(ginKeyPreOutputKeepaliveStop); ok {
		if stop, ok := v.(func()); ok && stop != nil {
			stop()
		}
	}
}

// isOfficialOAuthAccount reports accounts whose clients rely on staged
// account headers (Codex turn state) or official OAuth semantics; their
// responses are never committed early (D21).
func isOfficialOAuthAccount(account *service.Account) bool {
	if account == nil {
		return false
	}
	return account.Type == service.AccountTypeOAuth || account.Type == service.AccountTypeSetupToken
}

// inboundIsChatCompletions reports Chat Completions routes.
func inboundIsChatCompletions(c *gin.Context) bool {
	if c == nil {
		return false
	}
	p := strings.TrimRight(c.FullPath(), "/")
	if p == "" && c.Request != nil && c.Request.URL != nil {
		p = strings.TrimRight(c.Request.URL.Path, "/")
	}
	return strings.HasSuffix(p, "/chat/completions")
}

// resilienceChatTerminalFrame reports whether a committed Chat Completions
// stream ends with the resilience terminal frame instead of the 0.3.0
// "event: error" frame.
func resilienceChatTerminalFrame(c *gin.Context) bool {
	return resilience.Current().KeepaliveOn() && inboundIsChatCompletions(c)
}

// writeChatCompletionsErrorSSE ends a committed Chat Completions stream
// (design §7.6): one data line with the error and its original HTTP status,
// then [DONE].
func writeChatCompletionsErrorSSE(c *gin.Context, errType, code, message string, status int) bool {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return false
	}
	if !beginResilienceTerminal(c) {
		return true
	}
	errObj := gin.H{"type": errType, "message": message, "status": status}
	if code != "" {
		errObj["code"] = code
	}
	payload, err := json.Marshal(gin.H{"error": errObj})
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\ndata: [DONE]\n\n", payload); err != nil {
		_ = c.Error(err)
	}
	flusher.Flush()
	return true
}
