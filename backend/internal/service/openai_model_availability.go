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

// failoverOpenAIModelAvailability is shared by native and passthrough HTTP
// routing, after local request repairs and compact fallback. It owns the
// decision, observation and model cooldown together; stream events keep their
// existing policy. A bare forwarder has no account-selection owner.
func (s *OpenAIGatewayService) failoverOpenAIModelAvailability(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, body []byte, model string, passthrough bool) *UpstreamFailoverError {
	if s == nil || s.accountRepo == nil || !account.IsOpenAICompatible() || resp == nil ||
		openAIStreamClientOutputStarted(c, false) || !isOpenAIModelAvailabilityError(resp.StatusCode, "", body) {
		return nil
	}
	s.recordOpenAIModelAvailabilityFailure(account, model)
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
		category = string(resilience.CategoryModelUnsupported)
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
		Kind:                 "failover",
		Message:              message,
		Detail:               detail,
		UpstreamResponseBody: detail,
		ResilienceCategory:   category,
	})
	return annotateUpstreamFailoverAccount(&UpstreamFailoverError{
		StatusCode:        resp.StatusCode,
		ResponseHeaders:   resp.Header.Clone(),
		ResponseBody:      body,
		Scope:             GatewayFailureScopeAccount,
		Reason:            openAIModelUnavailableReason,
		NextAccountAction: NextAccountRetry,
	}, account)
}

// isOpenAIModelAvailabilityError shares the compact fallback's general
// availability phrases. Only error-message fields count; echoed request
// content must not change routing. Request rejections always take precedence.
func isOpenAIModelAvailabilityError(status int, message string, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return false
	}
	if openAIStreamRequestRejected(body, message) {
		return false
	}
	if isExplicitOpenAIModelAvailabilityMessage(message) ||
		isExplicitOpenAIModelAvailabilityMessage(extractUpstreamErrorMessage(body)) ||
		isExplicitOpenAIModelAvailabilityMessage(gjson.GetBytes(body, "response.error.message").String()) {
		return true
	}
	return !gjson.ValidBytes(body) && isExplicitOpenAIModelAvailabilityMessage(string(body))
}

// recordOpenAIModelAvailabilityFailure is used only by pre-output failover.
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
