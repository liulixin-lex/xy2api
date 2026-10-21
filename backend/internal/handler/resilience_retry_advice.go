package handler

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// retryAdviceAdvisor reads only process-local candidate and penalty snapshots.
// It must not call the legacy advisor, which can load accounts from storage.
type retryAdviceAdvisor interface {
	RetryAdvice(context.Context, *int64, string) resilience.RetryAdvice
}

// installResilienceRetryAdvice runs before selection. The closure retains
// immutable defaults and a service, never the pooled Gin context or API key.
func installResilienceRetryAdvice(c *gin.Context, diag any, apiKey *service.APIKey, model string) {
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
	c.Request = c.Request.WithContext(resilience.WithRetryAdviceProvider(c.Request.Context(), func(ctx context.Context) resilience.RetryAdvice {
		if !resilience.Current().FailedFrameRetryAfterOn() || ctx.Err() != nil {
			return resilience.RetryAdvice{}
		}
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
	}))
}

// prepareResilienceRetryAdvice fixes the final handler result before a
// downstream wrapper can synthesize its terminal frame.
func prepareResilienceRetryAdvice(c *gin.Context, diag any, apiKey *service.APIKey, model string) {
	if c == nil || c.Request == nil || !resilience.Current().FailedFrameRetryAfterOn() {
		return
	}
	installResilienceRetryAdvice(c, diag, apiKey, model)
	resilience.SetRetryAdvice(c.Request.Context(), resilience.ResolveRetryAdvice(c.Request.Context()))
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
	advice := resilience.ResolveRetryAdvice(c.Request.Context())
	// Invalid input and authentication errors are not candidate failures.
	switch errType {
	case "invalid_request_error", "authentication_error", "permission_error":
		advice = resilience.RetryAdvice{Suppress: true, Source: "client_error"}
		resilience.SetRetryAdvice(c.Request.Context(), advice)
		advice = resilience.RetryAdviceFrom(c.Request.Context())
	}
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
