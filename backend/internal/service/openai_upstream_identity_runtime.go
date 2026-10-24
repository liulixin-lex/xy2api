package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const openAIIdentityCapacity = 65536
const openAIIdentityRejectTTL = 24 * time.Hour

// Process-local safety state, never consulted while L-01 is disabled. Neither
// rejection memory nor the fan-in limiter changes routing, retry or penalty state.
type openAIUpstreamIdentityRuntime struct {
	mu             sync.Mutex
	inflight       map[string]int
	rejected       map[int64]time.Time
	rejectAllUntil time.Time
	poisoned       bool
}

type openAIUpstreamIdentityLease struct {
	state    *openAIUpstreamIdentityRuntime
	session  string
	released atomic.Bool
}

func (r *openAIUpstreamIdentityRuntime) acquire(session string, account int64, limit int, now time.Time) (*openAIUpstreamIdentityLease, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.rejectAllUntil) || now.Before(r.rejected[account]) {
		return nil, "upstream_rejects"
	}
	// Zero is only possible in old hand-built snapshots, not parsed policies.
	if limit == 0 {
		limit = 2
	}
	if r.poisoned || session == "" || limit < 1 || limit > 16 {
		return nil, "fanin_cap"
	}
	n, exists := r.inflight[session]
	if n < 0 || exists && n == 0 || n > 16 {
		r.poisoned = true
		return nil, "fanin_cap"
	}
	if n >= limit || !exists && len(r.inflight) >= openAIIdentityCapacity {
		return nil, "fanin_cap"
	}
	if r.inflight == nil {
		r.inflight = make(map[string]int)
	}
	r.inflight[session] = n + 1
	return &openAIUpstreamIdentityLease{state: r, session: session}, "none"
}

func (l *openAIUpstreamIdentityLease) release() {
	if l == nil || l.released.Swap(true) {
		return
	}
	r := l.state
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.inflight[l.session]
	if !ok || n <= 0 || n > 16 {
		r.poisoned = true
		return
	}
	if n == 1 {
		delete(r.inflight, l.session)
	} else {
		r.inflight[l.session] = n - 1
	}
}

func (r *openAIUpstreamIdentityRuntime) reject(account int64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.rejected[account]) || now.Before(r.rejectAllUntil) {
		return false
	}
	if r.rejected == nil {
		r.rejected = make(map[int64]time.Time)
	}
	if len(r.rejected) >= openAIIdentityCapacity {
		for id, until := range r.rejected {
			if !now.Before(until) {
				delete(r.rejected, id)
			}
		}
	}
	until := now.Add(openAIIdentityRejectTTL)
	if len(r.rejected) >= openAIIdentityCapacity {
		// Do not evict a still-protected account and silently resume injection.
		r.rejectAllUntil = until
	} else {
		r.rejected[account] = until
	}
	return true
}

func (s *OpenAIGatewayService) reserveOpenAIUpstreamIdentity(c *gin.Context, account *Account, d openAIUpstreamIdentityDecision) (*openAIUpstreamIdentityLease, string) {
	return s.openaiIdentityRuntime.acquire(d.RoutingSession, account.ID, CachePolicyFromGin(c).UpstreamIdentity().MaxInflightPerKey, time.Now())
}

// Only the known injected attempt can learn a rejection. Returning a plain error
// after writing the baseline 400 format prevents every recovery/failover retry.
func (s *OpenAIGatewayService) handleOpenAIUpstreamIdentityRejection(c *gin.Context, account *Account, resp *http.Response, body []byte, applied bool, writeCompatError compatErrorWriter) (bool, error) {
	if !applied || resp.StatusCode != http.StatusBadRequest {
		return false, nil
	}
	param := gjson.GetBytes(body, "error.param").String()
	message := gjson.GetBytes(body, "error.message").String()
	if param != "prompt_cache_key" && !bytes.Contains(bytes.ToLower([]byte(message)), []byte("prompt_cache_key")) {
		return false, nil
	}
	resilience.Count("l01_upstream_reject_total", 0, account.ID, "")
	if s.openaiIdentityRuntime.reject(account.ID, time.Now()) {
		logger.L().Warn("L-01 upstream rejected injected prompt_cache_key; injection disabled for 24h", zap.Int64("account_id", account.ID), zap.String("alert", "l01_upstream_reject"))
	}
	clientBody := restoreOpenAIUpstreamIdentityErrorBody(c, resp, body)
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(clientBody))
	if upstreamMsg == "" && writeCompatError != nil {
		upstreamMsg = fmt.Sprintf("Upstream error: %d", resp.StatusCode)
	}
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	MarkResponseCommitted(c)
	if writeCompatError != nil {
		writeCompatError(c, resp.StatusCode, "invalid_request_error", upstreamMsg)
	} else {
		writeOpenAIUpstreamClientError(c, resp.StatusCode, clientBody, upstreamMsg)
	}
	return true, fmt.Errorf("L-01 injected prompt_cache_key rejected: status 400")
}

func restoreOpenAIUpstreamIdentityErrorBody(c *gin.Context, resp *http.Response, body []byte) []byte {
	restored := restoreOpenAIUpstreamIdentityLiteralForClient(c, body, "error_body")
	if !bytes.Equal(restored, body) && resp != nil {
		resp.ContentLength = int64(len(restored))
		if resp.Header.Get("Content-Length") != "" {
			resp.Header.Set("Content-Length", strconv.Itoa(len(restored)))
		}
	}
	return restored
}
