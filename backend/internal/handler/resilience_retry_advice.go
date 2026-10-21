package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Only copyFailoverRetryAfter may supply an upstream recovery hint. Legacy
// HTTP defaults are not evidence that an account will recover.
const ginKeyResilienceUpstreamRetryAfter = "xy_resilience_upstream_retry_after"

// retryAdviceAdvisor reads only process-local candidate and penalty snapshots.
// It must not call the legacy advisor, which can load accounts from storage.
type retryAdviceAdvisor interface {
	RetryAdvice(context.Context, *int64, string) resilience.RetryAdvice
}

// installResilienceRetryAdvice runs before selection. The closure retains
// immutable defaults and a service, never the pooled Gin context or API key.
func installResilienceRetryAdvice(c *gin.Context, diag any, apiKey *service.APIKey, model string) {
	installRetryAdviceProvider(c, diag, apiKey, model, 0)
}

// installRetryAdviceProvider installs the lazy candidate advice. recovery is
// the last upstream's proven wait in seconds, used only when the candidates
// give no answer (RA-1).
func installRetryAdviceProvider(c *gin.Context, diag any, apiKey *service.APIKey, model string, recovery int) {
	if c == nil || c.Request == nil {
		return
	}
	advisor, ok := diag.(retryAdviceAdvisor)
	if !ok {
		return
	}
	var initialGroup int64
	if apiKey != nil && apiKey.GroupID != nil {
		initialGroup = *apiKey.GroupID
	}
	candidates := func(ctx context.Context) resilience.RetryAdvice {
		st := resilience.StateFrom(ctx)
		if st.ConfigError() {
			return resilience.RetryAdvice{Suppress: true, Source: "config_rejection"}
		}
		group := st.GroupID()
		if group == 0 {
			group = initialGroup
		}
		currentModel := st.Model()
		if currentModel == "" {
			currentModel = model
		}
		if group == 0 {
			return resilience.RetryAdvice{Source: "unknown"}
		}
		return advisor.RetryAdvice(ctx, &group, currentModel)
	}
	c.Request = c.Request.WithContext(resilience.WithRetryAdviceProvider(c.Request.Context(), func(ctx context.Context) resilience.RetryAdvice {
		if !resilience.Current().FailedFrameRetryAfterOn() || ctx.Err() != nil {
			return resilience.RetryAdvice{}
		}
		advice := candidates(ctx)
		if recovery > 0 && !advice.Suppress && advice.Seconds <= 0 && ctx.Err() == nil {
			advice = resilience.RetryAdvice{Seconds: recovery, Source: "recovery"}
		}
		return advice
	}))
}

// prepareResilienceRetryAdvice prepares the final handler advice for a
// failed frame. It reads the upstream hint now, because the Retry-After that
// proves it is rewritten right after; the candidates are queried only when a
// failed frame is written, by the handler or a downstream wrapper (P2-10).
// A plain JSON exit never carries the advice and never runs the provider.
func prepareResilienceRetryAdvice(c *gin.Context, diag any, apiKey *service.APIKey, model string) {
	if c == nil || c.Request == nil || !resilience.Current().FailedFrameRetryAfterOn() {
		return
	}
	installRetryAdviceProvider(c, diag, apiKey, model, upstreamRecoverySeconds(c))
}

func upstreamRecoverySeconds(c *gin.Context) int {
	value := c.GetString(ginKeyResilienceUpstreamRetryAfter)
	if value == "" || value != c.Writer.Header().Get("Retry-After") {
		return 0
	}
	var wait time.Duration
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		wait = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		wait = time.Until(at)
	}
	if wait <= 0 {
		return 0
	}
	return resilience.ClampRetryAfter(wait, 2, 10)
}

func exhaustedRetryModel(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if model := resilience.StateFrom(c.Request.Context()).Model(); model != "" {
		return model
	}
	return requestModel(c)
}

func suppressResilienceRetryAdvice(c *gin.Context, source string) {
	if c == nil || c.Request == nil {
		return
	}
	c.Writer.Header().Del("Retry-After")
	c.Set(ginKeyResilienceRetryAfter, 0)
	resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: source})
}

// Terminal billing and policy failures must not inherit an earlier delay.
// Concurrent balance reservations remain retryable and retain their own hint.
func suppressResilienceTerminalReason(c *gin.Context, errType, code string) bool {
	if !resilience.Current().ErrorsOn() {
		return false
	}
	for _, value := range []string{errType, code} {
		switch strings.ToLower(value) {
		case errTypeInsufficientBalance, "insufficient_quota":
			suppressResilienceRetryAdvice(c, "balance_terminal")
			return true
		case codeContentPolicyViolation:
			suppressResilienceRetryAdvice(c, "content_rejection")
			return true
		}
	}
	return false
}

func responsesFailedRetryAdvice(c *gin.Context, errType, code string) resilience.RetryAdvice {
	suppressResilienceTerminalReason(c, errType, code)
	// All handler stream exits record the intended status before writing;
	// the wire status may already be 200 because of a heartbeat.
	streamErr, _ := service.GetOpsStreamError(c)
	if !resilience.RetryAdviceEligible(streamErr.IntendedStatus, errType, mapResponsesErrorCode(errType, code)) {
		suppressResilienceRetryAdvice(c, "non_retryable")
	}
	advice := resilience.ResolveRetryAdvice(c.Request.Context())
	// The frame's advice is final for the request.
	resilience.SetRetryAdvice(c.Request.Context(), advice)
	c.Writer.Header().Del("Retry-After")
	c.Set(ginKeyResilienceRetryAfter, 0)
	if headers := advice.Headers(); headers != nil {
		c.Header("Retry-After", headers["retry-after"])
		c.Set(ginKeyResilienceRetryAfter, advice.Seconds)
	}
	return advice
}

func prepareExhaustedRetryAdvice(c *gin.Context, diag any) {
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	prepareResilienceRetryAdvice(c, diag, apiKey, exhaustedRetryModel(c))
}
