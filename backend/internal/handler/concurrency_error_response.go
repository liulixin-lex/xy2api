package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Only transient slot/queue failures receive this short recovery hint.
func applyConcurrencyRetryHint(c *gin.Context, status int, code string) {
	if c == nil || status != http.StatusTooManyRequests || (code != gatewayQueueFullCode && code != gatewayConcurrencyLimitCode) {
		return
	}
	if c.Request != nil && resilience.RetryAdviceFrom(c.Request.Context()).Suppress {
		return
	}
	if c.Writer.Header().Get("Retry-After") == "" {
		c.Header("Retry-After", "5")
		c.Set(ginKeyResilienceRetryAfter, 5)
		if c.Request != nil {
			resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 5, Source: "concurrency"})
		}
	}
}

const statusClientClosedRequest = 499

const (
	gatewayQueueFullCode        = "gateway_queue_full"
	gatewayConcurrencyLimitCode = "gateway_concurrency_limit"
)

func concurrencyErrorResponse(err error, slotType string) (int, string, string, string) {
	var waitQueueFullErr *WaitQueueFullError
	if errors.As(err, &waitQueueFullErr) {
		return http.StatusTooManyRequests, "rate_limit_error", gatewayQueueFullCode,
			"Too many pending requests, please retry later"
	}

	var concurrencyErr *ConcurrencyError
	if errors.As(err, &concurrencyErr) {
		if concurrencyErr.SlotType != "" {
			slotType = concurrencyErr.SlotType
		}
		return http.StatusTooManyRequests, "rate_limit_error", gatewayConcurrencyLimitCode,
			fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType)
	}

	if errors.Is(err, context.Canceled) {
		return statusClientClosedRequest, "api_error", "", "context canceled"
	}

	return http.StatusServiceUnavailable, "api_error", "", "Service temporarily unavailable, please retry later"
}
