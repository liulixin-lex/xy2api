package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/payment"
	"github.com/liulixin-lex/xy2api/internal/payment/provider"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
)

// StripeHostedWebhook does not acknowledge lookup or processing failures.
func (h *PaymentWebhookHandler) StripeHostedWebhook(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("instance_id"), 10, 64)
	if err != nil || id <= 0 {
		c.String(http.StatusBadRequest, "invalid instance")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBodySize))
	if err != nil {
		c.String(http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	p, err := h.paymentService.HostedWebhookProvider(ctx, id)
	if err != nil {
		c.String(http.StatusServiceUnavailable, "provider unavailable")
		return
	}
	n, err := p.VerifyNotification(ctx, string(body), map[string]string{"stripe-signature": c.GetHeader("Stripe-Signature")})
	if err != nil {
		if errors.Is(err, provider.ErrHostedWebhookRetry) {
			c.String(http.StatusServiceUnavailable, "upstream query incomplete")
			return
		}
		c.String(http.StatusBadRequest, "event not accepted")
		return
	}
	if n != nil {
		if err = h.paymentService.HandlePaymentNotification(ctx, n, payment.TypeStripeHosted); err != nil {
			c.String(http.StatusServiceUnavailable, "processing incomplete")
			return
		}
	}
	c.String(http.StatusOK, "ok")
}
func (h *PaymentHandler) ResumeStripeHostedOrder(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid order")
		return
	}
	result, err := h.paymentService.ResumeStripeHostedOrder(c.Request.Context(), id, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, result)
}
