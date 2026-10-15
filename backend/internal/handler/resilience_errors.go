package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Internal error types translated by the response writers (design §9.5).
const (
	// errTypeInsufficientBalance is the user's own balance being too low.
	errTypeInsufficientBalance = "insufficient_balance"
	// errTypeBalanceReserved is a positive balance held by in-flight
	// requests; it is written as rate_limit_error.
	errTypeBalanceReserved = "concurrent_balance_reserved"

	// userInsufficientBalanceMessage keeps the "insufficient balance" words so
	// downstream sub2api-style sites still recognise it, while naming this
	// platform so users know whose balance ran out.
	userInsufficientBalanceMessage = resilience.UserInsufficientBalanceMessage

	// ginKeyResilienceRetryAfter carries the Retry-After seconds chosen for a
	// no-account response to the writers, which add the message hint.
	ginKeyResilienceRetryAfter = "xy_resilience_retry_after"

	// ginKeyResilienceTurnCapped marks a failure that ends the client's turn
	// (design §9.7); the writers give it the code server_is_overloaded.
	ginKeyResilienceTurnCapped = resilience.GinKeyTurnCapped

	// codeServerIsOverloaded ends a Codex turn when no delay is given: it is
	// only retried with a server-advised delay.
	codeServerIsOverloaded = "server_is_overloaded"
)

// resilienceTranslateError maps internal error types to the client-facing
// type and code, and appends the retry hint to no-account messages.
func resilienceTranslateError(c *gin.Context, status int, errType, code, message string) (string, string, string) {
	switch errType {
	case errTypeInsufficientBalance:
		if code == "" {
			code = "INSUFFICIENT_BALANCE"
		}
	case errTypeBalanceReserved:
		errType = "rate_limit_error"
		if code == "" {
			code = "concurrent_balance_reserved"
		}
	}
	if c != nil && c.GetBool(ginKeyResilienceTurnCapped) {
		code = codeServerIsOverloaded
	}
	if c != nil && (status == http.StatusServiceUnavailable || resilienceTerminalHint(c, status)) {
		if secs := c.GetInt(ginKeyResilienceRetryAfter); secs > 0 && !strings.Contains(strings.ToLower(message), "try again in") {
			message = strings.TrimRight(strings.TrimSpace(message), ". ") + fmt.Sprintf(". Please try again in %ds.", secs)
		}
	}
	return errType, code, message
}

// resilienceTerminalHint reports whether a failure is written into an
// already committed stream, where the message is the only way to tell the
// client when to retry (design §7.6).
func resilienceTerminalHint(c *gin.Context, status int) bool {
	if c == nil || c.Writer == nil || !resilience.Current().KeepaliveOn() {
		return false
	}
	return (status == http.StatusTooManyRequests || status >= http.StatusInternalServerError) && c.Writer.Written()
}

// resilienceResponsesCode maps a committed Responses failure to the codes
// Codex understands (design §7.6): rate limits and overload are retried as
// rate_limit_exceeded, other upstream failures as server_error. It returns
// "" when the 0.3.0 mapping applies.
func resilienceResponsesCode(errType, code string) string {
	if code != "" || !resilience.Current().KeepaliveOn() {
		return ""
	}
	switch errType {
	case "rate_limit_error", "overloaded_error":
		return "rate_limit_exceeded"
	case "upstream_error", "api_error", "server_error", "":
		return "server_error"
	}
	return ""
}

// noAccountRetryAdvisor is implemented by the gateway services.
type noAccountRetryAdvisor interface {
	NoAccountRetryAfter(ctx context.Context, groupID *int64, model string) time.Duration
}

// applyNoAccountRetryHints sets the retry headers of a no-account response
// (design §9.1): configuration failures get Retry-After 60 and
// x-should-retry: false; temporary ones the clamped time until the group's
// earliest recovery. Status codes and messages stay as they are.
func applyNoAccountRetryHints(c *gin.Context, diag any, apiKey *service.APIKey, routingModel string, cls noAccountErrorClassification) {
	cfg := resilience.Current()
	if c == nil || c.Request == nil || !cfg.ErrorsOn() || cls.ModelNotFound {
		return
	}
	ctx := c.Request.Context()
	if resilience.StateFrom(ctx).ConfigError() {
		c.Header("Retry-After", strconv.Itoa(cfg.Errors.ConfigRetryAfterSeconds))
		c.Header("x-should-retry", "false")
		return
	}
	if resilienceCapTurn(c) {
		return
	}
	setNoAccountRetryAfter(c, diag, apiKey, routingModel)
}

// setNoAccountRetryAfter sets Retry-After to the clamped time until the
// group's earliest recovery.
func setNoAccountRetryAfter(c *gin.Context, diag any, apiKey *service.APIKey, routingModel string) {
	cfg := resilience.Current()
	ctx := c.Request.Context()
	var wait time.Duration
	if advisor, ok := diag.(noAccountRetryAdvisor); ok && apiKey != nil {
		wait = advisor.NoAccountRetryAfter(ctx, apiKey.GroupID, routingModel)
	}
	if wait <= 0 {
		// Nothing recovers on its own: ask clients to back off longest.
		wait = time.Duration(cfg.Errors.NoAccountRetryAfter.Max) * time.Second
	}
	secs := resilience.ClampRetryAfter(wait, cfg.Errors.NoAccountRetryAfter.Min, cfg.Errors.NoAccountRetryAfter.Max)
	c.Header("Retry-After", strconv.Itoa(secs))
	c.Set(ginKeyResilienceRetryAfter, secs)
}

// applyExhaustedRetryHint gives an exhausted failover that ends in 429 or a
// 5xx a server-capped Retry-After (design §9.1): an upstream value copied
// onto the response is clamped (a 502 used to pass a Cloudflare page's 60 s,
// or any larger value, straight through), otherwise the time to the group's
// earliest recovery is used. Clients such as Codex retry a 5xx without
// Retry-After many times in a row, so 502/504 get the hint too. It returns
// the status to write: a capped turn (design §9.7) ends a 5xx as 503.
func applyExhaustedRetryHint(c *gin.Context, diag any, status int) int {
	cfg := resilience.Current()
	if c == nil || c.Request == nil || !cfg.ErrorsOn() ||
		(status != http.StatusTooManyRequests && status < http.StatusInternalServerError && !resilienceTerminalHint(c, status)) {
		return status
	}
	if resilienceCapTurn(c) {
		if status >= http.StatusInternalServerError {
			return http.StatusServiceUnavailable
		}
		return status
	}
	lo, hi := cfg.Errors.NoAccountRetryAfter.Min, cfg.Errors.NoAccountRetryAfter.Max
	if v := strings.TrimSpace(c.Writer.Header().Get("Retry-After")); v != "" {
		secs := 0
		if n, err := strconv.Atoi(v); err == nil {
			secs = resilience.ClampRetryAfter(time.Duration(n)*time.Second, lo, hi)
		} else if at, err := http.ParseTime(v); err == nil {
			secs = resilience.ClampRetryAfter(time.Until(at), lo, hi)
		}
		if secs <= 0 {
			secs = lo
		}
		c.Header("Retry-After", strconv.Itoa(secs))
		c.Set(ginKeyResilienceRetryAfter, secs)
		return status
	}
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	setNoAccountRetryAfter(c, diag, apiKey, "")
	return status
}

// resilienceCapTurn counts an exhausted failure toward the conversation's
// streak (design §9.7). Codex follows every Retry-After and would retry a
// failed turn about 30 times; from the cap on, the failure is final: no
// Retry-After, x-should-retry: false (OpenAI and Anthropic SDKs, Claude
// Code) and the code server_is_overloaded, which Codex does not retry
// without a delay. It reports whether this failure reached the cap.
func resilienceCapTurn(c *gin.Context) bool {
	cfg := resilience.Current()
	tc := cfg.Errors.TurnCap
	if c == nil || c.Request == nil || !cfg.ErrorsOn() || !tc.Enabled {
		return false
	}
	st := resilience.StateFrom(c.Request.Context())
	key := st.TurnKey()
	if key == "" {
		return false
	}
	st.MarkTurnFailed()
	n := resilience.ProcessTurnCap().Fail(key, time.Duration(tc.WindowSeconds)*time.Second, time.Now())
	if n < tc.Failures {
		return false
	}
	c.Writer.Header().Del("Retry-After")
	c.Header("x-should-retry", "false")
	c.Set(ginKeyResilienceTurnCapped, true)
	resilience.Count(resilience.MetricTurnCap, st.GroupID(), 0, "")
	slog.Warn("resilience.turn_cap", "group_id", st.GroupID(), "failures", n)
	return true
}

// stashResilienceErrorResponse records a JSON error response for the per-key
// fast fail (design §9.3). Only 503 responses are ever replayed.
func stashResilienceErrorResponse(c *gin.Context, status int, errType, message string, obj any) {
	if c == nil || status != http.StatusServiceUnavailable || !resilience.Current().ErrorsOn() {
		return
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return
	}
	c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: status, ErrType: errType, Message: message, Body: body})
}

// resilienceHideUpstreamBalance turns an exhausted failover whose last
// failure was an upstream balance problem into the standard temporary 503
// (design §9.4). Administrators keep the original text through the ops
// upstream error. It reports false when the failure is not a balance one.
func resilienceHideUpstreamBalance(c *gin.Context, diag any, failoverErr *service.UpstreamFailoverError) (int, string, string, bool) {
	cfg := resilience.Current()
	if c == nil || failoverErr == nil || !cfg.ErrorsOn() {
		return 0, "", "", false
	}
	isBalance := failoverErr.Reason == service.OpenAIImagesInsufficientBalanceReason ||
		service.ClassifyFailoverError(failoverErr).Category == resilience.CategoryBalance
	if !isBalance {
		return 0, "", "", false
	}
	upstreamMsg := service.SanitizeUpstreamErrorMessage(service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody))
	if upstreamMsg == "" {
		upstreamMsg = strings.TrimSpace(failoverErr.ClientMessage)
	}
	service.SetOpsUpstreamError(c, failoverErr.StatusCode, upstreamMsg, "")
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	applyNoAccountRetryHints(c, diag, apiKey, "", noAccountErrorClassification{Status: http.StatusServiceUnavailable})
	return http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", true
}
