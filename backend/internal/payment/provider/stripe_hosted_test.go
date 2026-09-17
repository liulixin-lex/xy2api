//go:build unit

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/payment"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"
)

func hostedSessionFixture() *stripe.CheckoutSession {
	return &stripe.CheckoutSession{ID: "cs_test_1", URL: "https://checkout.stripe.com/c/pay/cs_test_1", ExpiresAt: time.Now().Add(time.Hour).Unix(), Mode: "payment", Status: "open", PaymentStatus: "unpaid", Currency: "cny", AmountTotal: 8000, ClientReferenceID: "order-1", Metadata: map[string]string{"orderId": "order-1", "providerKey": "stripe_hosted", "providerInstanceId": "1", "accountId": "acct_test"}, PaymentIntent: &stripe.PaymentIntent{ID: "pi_1", Status: "processing"}}
}

func hostedTestProvider(t *testing.T, handle http.HandlerFunc) *StripeHosted {
	t.Helper()
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), MaxNetworkRetries: stripe.Int64(0)})
	p, err := NewStripeHosted("1", map[string]string{"secretKey": "sk_test_example", "webhookSecret": "whsec_example", "currency": "CNY"})
	require.NoError(t, err)
	p.client = stripe.NewClient("sk_test_example", stripe.WithBackends(&stripe.Backends{API: backend}))
	return p
}

func TestStripeHostedCreateFreezesAmountAndRedirect(t *testing.T) {
	var requests []url.Values
	var keys []string
	p := hostedTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/account" {
			_, _ = w.Write([]byte(`{"id":"acct_test"}`))
			return
		}
		require.Equal(t, "/v1/checkout/sessions", r.URL.Path)
		require.NoError(t, r.ParseForm())
		requests = append(requests, r.PostForm)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_ = json.NewEncoder(w).Encode(hostedSessionFixture())
	})
	req := payment.CreatePaymentRequest{OrderID: "order-1", Amount: "80.00", Subject: "Balance", ReturnURL: "https://app.example/payment/result?order_id=1", AccountID: "acct_test", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	for i := 0; i < 2; i++ {
		result, err := p.CreatePayment(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, "cs_test_1", result.TradeNo)
		require.Empty(t, result.ClientSecret)
	}
	require.Equal(t, requests[0], requests[1])
	require.Equal(t, keys[0], keys[1])
	v := requests[0]
	require.Equal(t, "8000", v.Get("line_items[0][price_data][unit_amount]"))
	require.Equal(t, "hosted_page", v.Get("ui_mode"))
	require.Equal(t, "payment", v.Get("mode"))
	require.Equal(t, "false", v.Get("adaptive_pricing[enabled]"))
	require.Equal(t, "false", v.Get("automatic_tax[enabled]"))
	require.Empty(t, v.Get("payment_method_types[0]"))
	require.Contains(t, v.Get("success_url"), "{CHECKOUT_SESSION_ID}")
	require.NotContains(t, v.Get("cancel_url"), "status=success")
}

func TestStripeHostedWebhookValidationAndCanonicalState(t *testing.T) {
	cs := hostedSessionFixture()
	cs.Status = "complete"
	cs.PaymentStatus = "paid"
	p := hostedTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cs)
	})
	payload := hostedSessionFixture()
	payload.Status = "complete"
	event := map[string]any{"id": "evt_1", "object": "event", "type": "checkout.session.completed", "api_version": stripe.APIVersion, "livemode": false, "data": map[string]any{"object": payload}}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: data, Secret: "whsec_example"})
	n, err := p.VerifyNotification(context.Background(), string(data), map[string]string{"stripe-signature": signed.Header})
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusSuccess, n.Status)
	require.Equal(t, "8000", n.Metadata["amount_minor"])
	_, err = p.VerifyNotification(context.Background(), string(data)+" ", map[string]string{"stripe-signature": signed.Header})
	require.Error(t, err)
	expired := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: data, Secret: "whsec_example", Timestamp: time.Now().Add(-10 * time.Minute)})
	_, err = p.VerifyNotification(context.Background(), string(data), map[string]string{"stripe-signature": expired.Header})
	require.Error(t, err)
	cs.Metadata["providerInstanceId"] = "2"
	_, err = p.VerifyNotification(context.Background(), string(data), map[string]string{"stripe-signature": signed.Header})
	require.Error(t, err)
}

func TestStripeHostedStateMappingAndIdentity(t *testing.T) {
	p, err := NewStripeHosted("1", map[string]string{"secretKey": "sk_test_x", "webhookSecret": "whsec_x", "currency": "CNY"})
	require.NoError(t, err)
	for _, tc := range []struct {
		status stripe.CheckoutSessionStatus
		paid   stripe.CheckoutSessionPaymentStatus
		want   string
	}{{"open", "unpaid", "pending"}, {"complete", "unpaid", "processing"}, {"complete", "paid", "paid"}, {"expired", "unpaid", "expired"}} {
		cs := hostedSessionFixture()
		cs.Status = tc.status
		cs.PaymentStatus = tc.paid
		r, err := p.sessionResult(cs)
		require.NoError(t, err)
		require.Equal(t, tc.want, r.Status)
	}
	for _, mutate := range []func(*stripe.CheckoutSession){func(c *stripe.CheckoutSession) { c.Livemode = true }, func(c *stripe.CheckoutSession) { c.Currency = "usd" }, func(c *stripe.CheckoutSession) { c.ClientReferenceID = "other" }, func(c *stripe.CheckoutSession) { c.Mode = "subscription" }} {
		cs := hostedSessionFixture()
		mutate(cs)
		_, err := p.sessionResult(cs)
		require.Error(t, err)
	}
}

func TestStripeHostedRejectsRedirectAndRefundConfusion(t *testing.T) {
	for _, raw := range []string{"http://checkout.stripe.com/c/pay/cs_1", "https://checkout.stripe.com.evil.test/c/pay/cs_1", "https://user@checkout.stripe.com/c/pay/cs_1", "javascript:alert(1)", "https://checkout.stripe.com:8443/c/pay/cs_1"} {
		require.False(t, ValidStripeCheckoutURL(raw))
	}
	require.True(t, ValidStripeCheckoutURL("https://checkout.stripe.com/c/pay/cs_test_1#abc"))
	cs := hostedSessionFixture()
	cs.Status = "complete"
	cs.PaymentStatus = "paid"
	refundCalls := 0
	p := hostedTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
			_ = json.NewEncoder(w).Encode(cs)
			return
		}
		refundCalls++
		require.NoError(t, r.ParseForm())
		require.Equal(t, "pi_1", r.Form.Get("payment_intent"))
		require.Equal(t, "1200", r.Form.Get("amount"))
		_, _ = w.Write([]byte(`{"id":"re_1","status":"succeeded","amount":1200,"payment_intent":"pi_1","metadata":{"sessionId":"cs_test_1","orderId":"order-1"}}`))
	})
	_, err := p.Refund(context.Background(), payment.RefundRequest{TradeNo: "cs_test_1", OrderID: "other", Amount: "12.00"})
	require.Error(t, err)
	require.Zero(t, refundCalls)
	r, err := p.Refund(context.Background(), payment.RefundRequest{TradeNo: "cs_test_1", OrderID: "order-1", Amount: "12.00"})
	require.NoError(t, err)
	require.Equal(t, "success", r.Status)
	require.Equal(t, 1, refundCalls)
	require.Error(t, p.CancelPayment(context.Background(), "cs_test_1"))
}

func TestStripeHostedRefundTimeoutRecovery(t *testing.T) {
	cs := hostedSessionFixture()
	cs.Status = "complete"
	cs.PaymentStatus = "paid"
	var keys []string
	p := hostedTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
			_ = json.NewEncoder(w).Encode(cs)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"object":"list","data":[],"has_more":false,"url":"/v1/refunds"}`))
			return
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"uncertain"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"re_1","status":"succeeded","amount":1200,"payment_intent":"pi_1","metadata":{"sessionId":"cs_test_1","orderId":"order-1"}}`))
	})
	r, err := p.Refund(context.Background(), payment.RefundRequest{TradeNo: cs.ID, OrderID: "order-1", Amount: "12.00"})
	require.Error(t, err)
	require.Equal(t, payment.ProviderStatusPending, r.Status)
	r, err = p.QueryRefund(context.Background(), payment.RefundQueryRequest{TradeNo: cs.ID, OrderID: "order-1", Amount: "12.00", RetryUntil: time.Now().Add(time.Hour).Unix()})
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusSuccess, r.Status)
	require.Len(t, keys, 2)
	require.Equal(t, keys[0], keys[1])
	r, err = p.QueryRefund(context.Background(), payment.RefundQueryRequest{TradeNo: cs.ID, OrderID: "order-1", Amount: "12.00", RetryUntil: time.Now().Add(-time.Hour).Unix()})
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusPending, r.Status)
	require.Len(t, keys, 2)
}

func TestStripeHostedRejectsWrongAPIVersionAndRetriesUpstreamFailure(t *testing.T) {
	p := hostedTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"temporary"}}`))
	})
	for _, version := range []string{"2020-08-27", stripe.APIVersion} {
		data, err := json.Marshal(map[string]any{"id": "evt_1", "object": "event", "type": "checkout.session.completed", "api_version": version, "data": map[string]any{"object": hostedSessionFixture()}})
		require.NoError(t, err)
		signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: data, Secret: "whsec_example"})
		_, err = p.VerifyNotification(context.Background(), string(data), map[string]string{"stripe-signature": signed.Header})
		if version == stripe.APIVersion {
			require.ErrorIs(t, err, ErrHostedWebhookRetry)
		} else {
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrHostedWebhookRetry)
		}
	}
}
