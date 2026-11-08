package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const openAIModelUnavailableReason = GatewayFailureReason("openai_model_unavailable")
const openAIUpstreamRequestRestrictionReason = GatewayFailureReason(resilience.ReasonUpstreamRequestRestriction)

// IsOpenAIUpstreamRequestRestriction identifies the unpenalized 5xx request route.
func (e *UpstreamFailoverError) IsOpenAIUpstreamRequestRestriction() bool {
	return e != nil && e.Reason == openAIUpstreamRequestRestrictionReason
}

// failoverOpenAIModelAvailability is shared by native and passthrough HTTP
// routing, after local request repairs and compact fallback. It owns the
// decision and observation together. Provider request restrictions share this
// unpenalized exit without a model cooldown. Responses stream model errors
// enter through failoverOpenAIStreamModelAvailability. A bare forwarder has
// no account-selection owner.
func (s *OpenAIGatewayService) failoverOpenAIModelAvailability(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, body []byte, model string, passthrough bool, streamError ...bool) *UpstreamFailoverError {
	if s == nil || s.accountRepo == nil || !account.IsOpenAICompatible() || resp == nil ||
		openAIStreamClientOutputStarted(c, false) {
		return nil
	}
	reason, failureCategory := openAIModelUnavailableReason, resilience.CategoryModelUnsupported
	scope, action, kind := GatewayFailureScopeAccount, NextAccountRetry, "failover"
	switch {
	case resp.StatusCode == http.StatusNotFound:
		if classifyInferenceUpstreamHTTP(resp.StatusCode, resp.Header, body, "").Category != resilience.CategoryModelUnsupported {
			return nil
		}
	case isOpenAIModelAvailabilityError(resp.StatusCode, "", body):
	case !openAIStreamRequestRejected(body, "") && classifyUpstreamHTTP(resp.StatusCode, resp.Header, body, "").Category == resilience.CategoryUpstreamRequestRestriction:
		reason, failureCategory = openAIUpstreamRequestRestrictionReason, resilience.CategoryUpstreamRequestRestriction
	default:
		return nil
	}
	// Keep independent evidence for each failure class within this request.
	// A second upstream host establishes request scope except for inference
	// HTTP 404s: every remaining candidate may have a working model endpoint.
	// Stream-derived model errors retain the existing cross-host guard.
	host, previousHost := accountUpstreamHost(account), ""
	hostKey := "openai_request_failure_host_" + string(reason)
	if c != nil {
		previousHost = c.GetString(hostKey)
	}
	httpNotFound := resp.StatusCode == http.StatusNotFound && (len(streamError) == 0 || !streamError[0])
	if !httpNotFound && host != "" && previousHost != "" && host != previousHost {
		scope, action, kind = GatewayFailureScopeRequest, NextAccountStop, "http_error"
		failureCategory = resilience.CategoryClientError
	} else {
		if c != nil && host != "" {
			c.Set(hostKey, host)
		}
		if reason == openAIModelUnavailableReason {
			s.recordOpenAIModelAvailabilityFailure(account, model)
		}
	}
	body = s.redactAgentIdentitySensitiveBody(ctx, account, body)
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if message == "" && !gjson.ValidBytes(body) {
		message = sanitizeUpstreamErrorMessage(strings.TrimSpace(string(body)))
	}
	detail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(body), maxBytes)
	}
	category := ""
	if resilience.Current().FailureOn() {
		category = string(failureCategory)
	}
	setOpsUpstreamError(c, resp.StatusCode, message, detail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:              opsUpstreamProxyID(account),
		ProxyName:            opsUpstreamProxyName(account),
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          passthrough,
		Kind:                 kind,
		Message:              message,
		Detail:               detail,
		UpstreamResponseBody: detail,
		ResilienceCategory:   category,
	})
	return annotateUpstreamFailoverAccount(&UpstreamFailoverError{
		StatusCode:        resp.StatusCode,
		ResponseHeaders:   resp.Header.Clone(),
		ResponseBody:      body,
		Scope:             scope,
		Reason:            reason,
		NextAccountAction: action,
	}, account)
}

// isOpenAIModelAvailabilityError shares the compact fallback's general
// availability phrases and also accepts the protocol code model_not_found.
// Only error envelopes count; request rejections always take precedence.
func isOpenAIModelAvailabilityError(status int, message string, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound && (status < 500 || status > 599) {
		return false
	}
	if openAIStreamRequestRejected(body, message) {
		return false
	}
	if !openAIModelAvailabilityParamAllowed(body) {
		return false
	}
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		if openAIStreamRequestRejected([]byte(object.Raw), message) {
			return false
		}
	}
	if openAIModelNotFoundCode(body) {
		return true
	}
	if isExplicitOpenAIModelAvailabilityMessage(message) ||
		isExplicitOpenAIModelAvailabilityMessage(extractUpstreamErrorMessage(body)) ||
		isExplicitOpenAIModelAvailabilityMessage(gjson.GetBytes(body, "response.error.message").String()) {
		return true
	}
	return !gjson.ValidBytes(body) && isExplicitOpenAIModelAvailabilityMessage(string(body))
}

func openAIModelAvailabilityParamAllowed(body []byte) bool {
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		for _, path := range []string{"error.param", "response.error.param", "param"} {
			param := object.Get(path)
			if !param.Exists() || param.Type == gjson.Null {
				continue
			}
			if param.Type != gjson.String || (strings.TrimSpace(param.Str) != "" && strings.TrimSpace(param.Str) != "model") {
				return false
			}
		}
	}
	return true
}

// Inspect the HTTP/Responses envelope and one embedded JSON error message,
// matching the existing relay error convention. Never walk request content.
func openAIModelAvailabilityEnvelopes(body []byte) []gjson.Result {
	if !gjson.ValidBytes(body) {
		return nil
	}
	objects := []gjson.Result{gjson.ParseBytes(body)}
	for _, path := range []string{"error.message", "response.error.message"} {
		if inner := objects[0].Get(path); inner.Type == gjson.String && gjson.Valid(inner.Str) {
			objects = append(objects, gjson.Parse(inner.Str))
		}
	}
	if inner := openAITopLevelStreamError(body).Get("message"); inner.Type == gjson.String && gjson.Valid(inner.Str) {
		objects = append(objects, gjson.Parse(inner.Str))
	}
	return objects
}

func openAIModelNotFoundCode(body []byte) bool {
	for _, object := range openAIModelAvailabilityEnvelopes(body) {
		if strings.TrimSpace(openAITopLevelStreamError([]byte(object.Raw)).Get("code").String()) == "model_not_found" {
			return true
		}
		for _, path := range []string{"error.code", "response.error.code"} {
			if code := object.Get(path); code.Type == gjson.String && strings.TrimSpace(code.Str) == "model_not_found" {
				return true
			}
		}
	}
	return false
}

// Use the same bounded envelope scope as the code/param checks, with an
// explicit outer status taking precedence over an embedded error status.
func openAIModelAvailabilityExplicitStatus(payload []byte) (int, bool) {
	for _, object := range openAIModelAvailabilityEnvelopes(payload) {
		for _, path := range openAIStreamErrorStatusPaths {
			if status, ok := strictJSONIntegerStatus([]byte(object.Raw), path); ok && status >= 400 && status <= 599 {
				return status, true
			}
		}
	}
	return 0, false
}

// openAIStreamModelAvailabilityStatus uses the existing semantic status and
// Q2 predicate. Validated wrapper statuses retain priority, including 401/403;
// request rejections and input parameters are checked on the original payload.
func openAIStreamModelAvailabilityStatus(payload []byte, message string, wrappedStatus int) (int, bool) {
	status, defaulted := openAIStreamFailedEventSemanticStatusSource(payload, message, wrappedStatus)
	// Numeric protocol statuses, including 5xx, must not become a synthesized
	// 404. Keep the legacy semantic mapping unchanged for non-Q2 callers.
	if wrappedStatus == 0 {
		if explicit, ok := openAIModelAvailabilityExplicitStatus(payload); ok {
			status, defaulted = explicit, false
		}
	}
	if !isOpenAIModelAvailabilityError(status, message, payload) {
		return status, false
	}
	if defaulted && (openAIModelNotFoundCode(payload) ||
		isExplicitOpenAIModelNotFoundMessage(message) ||
		isExplicitOpenAIModelNotFoundMessage(extractUpstreamErrorMessage(payload)) ||
		isExplicitOpenAIModelNotFoundMessage(gjson.GetBytes(payload, "response.error.message").String())) {
		status = http.StatusNotFound
	}
	return status, true
}

// failoverOpenAIStreamModelAvailability only adapts model errors. Other stream
// failures, including generic invalid_request_error, retain their existing exit.
func (s *OpenAIGatewayService) failoverOpenAIStreamModelAvailability(ctx context.Context, c *gin.Context, account *Account, payload []byte, message string, headers http.Header, model string, passthrough bool, wrappedStatus int) *UpstreamFailoverError {
	status, matched := openAIStreamModelAvailabilityStatus(payload, message, wrappedStatus)
	if !matched {
		return nil
	}
	body := openAIStreamFailedEventPassthroughBody(payload, message)
	return s.failoverOpenAIModelAvailability(ctx, c, account, &http.Response{StatusCode: status, Header: headers}, body, model, passthrough, true)
}

// handleOpenAIStreamFailureAfterOutput is shared only by native/passthrough
// Responses. Model availability advances the same account-model cooldown as
// HTTP Q2, without replay or account-health penalties. Bare error/failed pairs
// still use the callers' existing terminal deferral to apply this once.
func (s *OpenAIGatewayService) handleOpenAIStreamFailureAfterOutput(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, payload []byte, message, model string, wrappedStatus int) {
	if _, matched := openAIStreamModelAvailabilityStatus(payload, message, wrappedStatus); matched && account.IsOpenAICompatible() {
		s.recordOpenAIModelAvailabilityFailure(account, model)
		noteResiliencePostOutputFailureWithCategory(ctx, s.rateLimitService, account, resp, postOutputModelUnavailable, resilience.CategoryModelUnsupported)
		return
	}
	s.handleOpenAIStreamTerminalAccountSideEffectsForFrame(c, account, payload, message, resp.Header, model, wrappedStatus)
	s.resilienceOpenAIFailedAfterOutput(ctx, account, resp, payload, message, true)
}

// recordOpenAIModelAvailabilityFailure is shared by HTTP and Responses SSE.
// It bypasses persistent model limits and all account-health penalties.
func (s *OpenAIGatewayService) recordOpenAIModelAvailabilityFailure(account *Account, model string) {
	decision := s.recordOpenAIAccountModelTransientFailure(account, model, time.Now())
	if decision.FailureStreak > 0 {
		slog.Warn("openai_model_transient_state",
			"account_id", account.ID,
			"model", openAIAccountModelTransientModel(model),
			"failure_streak", decision.FailureStreak,
			"cooldown_ms", decision.Cooldown.Milliseconds(),
			"block_scope", "account_model",
			"slow_streak", 0,
			"reason", string(openAIModelUnavailableReason),
		)
	}
}
