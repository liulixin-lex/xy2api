package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
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
	for k, vs := range resp.Header {
		for _, v := range vs {
			c.Writer.Header().Add(k, v)
		}
	}
	if secs := resp.RetryAfterSeconds(now); secs > 0 {
		c.Header("Retry-After", strconv.Itoa(secs))
	}
	c.Header("X-XY-Fast-Fail", "1")
	c.Data(resp.Status, "application/json", resp.Body)
	c.Abort()
	return true
}

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
	if status != http.StatusServiceUnavailable {
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
	retryAt := now
	if secs, err := strconv.Atoi(c.Writer.Header().Get("Retry-After")); err == nil && secs > 0 {
		retryAt = now.Add(time.Duration(secs) * time.Second)
	} else {
		retryAt = now.Add(time.Duration(cfg.Errors.FastFail.WindowSeconds) * time.Second)
	}
	tripped := resilience.ProcessFastFail().Observe(apiKeyID,
		resilience.Signature(er.Status, er.ErrType, er.Message),
		resilience.CachedResponse{Status: er.Status, Body: er.Body, Header: header, RetryAt: retryAt},
		time.Duration(cfg.Errors.FastFail.WindowSeconds)*time.Second, cfg.Errors.FastFail.Threshold, now)
	if tripped {
		slog.Warn("resilience.fast_fail", "api_key_id", apiKeyID, "status", er.Status, "error_type", er.ErrType, "retry_at", retryAt)
	}
}

// resilienceUserBalanceMessage is the middleware message for an empty user
// balance.
func resilienceUserBalanceMessage() string {
	if resilience.Current().ErrorsOn() {
		return resilience.UserInsufficientBalanceMessage
	}
	return "Insufficient account balance"
}

// resilienceNoteGroup records the API key's group on the request state so
// failover can find accounts sharing a relay host (design §6.3).
func resilienceNoteGroup(c *gin.Context, apiKey *service.APIKey) {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.GroupID == nil {
		return
	}
	resilience.StateFrom(c.Request.Context()).SetGroup(*apiKey.GroupID)
}
