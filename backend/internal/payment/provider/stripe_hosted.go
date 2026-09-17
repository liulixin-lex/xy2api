package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/payment"
	stripe "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"
)

var ErrHostedWebhookRetry = errors.New("hosted webhook upstream query failed")

// StripeHosted owns the Checkout Session lifecycle independently of Elements.
type StripeHosted struct {
	instanceID string
	config     map[string]string
	client     *stripe.Client
}

func NewStripeHosted(instanceID string, config map[string]string) (*StripeHosted, error) {
	for _, key := range []string{"secretKey", "webhookSecret"} {
		if strings.TrimSpace(config[key]) == "" {
			return nil, fmt.Errorf("stripe hosted config missing required key: %s", key)
		}
	}
	key := config["secretKey"]
	if !strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "sk_live_") && !strings.HasPrefix(key, "rk_test_") && !strings.HasPrefix(key, "rk_live_") {
		return nil, fmt.Errorf("stripe hosted requires a test or live server API key")
	}
	cfg := cloneStringMap(config)
	currency, err := payment.NormalizePaymentCurrency(cfg["currency"])
	if err != nil {
		return nil, err
	}
	cfg["currency"] = currency
	return &StripeHosted{instanceID: instanceID, config: cfg, client: stripe.NewClient(key)}, nil
}
func (s *StripeHosted) Name() string        { return "stripe 托管" }
func (s *StripeHosted) ProviderKey() string { return payment.TypeStripeHosted }
func (s *StripeHosted) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeStripeHosted}
}
func (s *StripeHosted) LiveMode() bool {
	return strings.HasPrefix(s.config["secretKey"], "sk_live_") || strings.HasPrefix(s.config["secretKey"], "rk_live_")
}
func (s *StripeHosted) MerchantIdentityMetadata() map[string]string {
	return map[string]string{"currency": s.config["currency"]}
}
func (s *StripeHosted) AccountID(ctx context.Context) (string, error) {
	a, err := s.client.V1Accounts.Retrieve(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("stripe hosted retrieve account: %w", err)
	}
	if a.ID == "" {
		return "", fmt.Errorf("stripe hosted account id missing")
	}
	return a.ID, nil
}

// ValidStripeCheckoutURL accepts only Stripe's standard hosted origin. Custom
// domains are deliberately not accepted without an explicit server allowlist.
func ValidStripeCheckoutURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && strings.HasPrefix(u.Path, "/c/pay/")
}
func (s *StripeHosted) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	amount, err := payment.AmountToMinorUnit(req.Amount, s.config["currency"])
	if err != nil || amount <= 0 {
		return nil, fmt.Errorf("stripe hosted invalid amount")
	}
	if req.AccountID == "" || req.ExpiresAt == 0 {
		return nil, fmt.Errorf("stripe hosted missing frozen order context")
	}
	account, err := s.AccountID(ctx)
	if err != nil {
		return nil, err
	}
	if account != req.AccountID {
		return nil, fmt.Errorf("stripe hosted account changed")
	}
	u, err := url.Parse(req.ReturnURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && (u.Scheme != "http" || s.LiveMode() || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1"))) {
		return nil, fmt.Errorf("stripe hosted invalid return origin")
	}
	q := u.Query()
	q.Set("checkout", "returned")
	u.RawQuery = q.Encode()
	cancelURL := u.String()
	q.Set("checkout", "complete")
	u.RawQuery = q.Encode()
	// Keep the literal placeholder; Stripe replaces it after payment.
	successURL := u.String() + "&session_id={CHECKOUT_SESSION_ID}"
	metadata := map[string]string{"orderId": req.OrderID, "providerInstanceId": s.instanceID, "providerKey": payment.TypeStripeHosted, "accountId": account}
	p := &stripe.CheckoutSessionCreateParams{
		Mode: stripe.String("payment"), UIMode: stripe.String(string(stripe.CheckoutSessionUIModeHostedPage)),
		ClientReferenceID: stripe.String(req.OrderID), Metadata: metadata,
		SuccessURL: stripe.String(successURL), CancelURL: stripe.String(cancelURL), ExpiresAt: stripe.Int64(req.ExpiresAt),
		AdaptivePricing:     &stripe.CheckoutSessionCreateAdaptivePricingParams{Enabled: stripe.Bool(false)},
		AutomaticTax:        &stripe.CheckoutSessionCreateAutomaticTaxParams{Enabled: stripe.Bool(false)},
		AllowPromotionCodes: stripe.Bool(false),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{Quantity: stripe.Int64(1), PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
			Currency: stripe.String(strings.ToLower(s.config["currency"])), UnitAmount: stripe.Int64(amount),
			ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: stripe.String(req.Subject)},
		}}},
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{Metadata: metadata},
	}
	p.SetIdempotencyKey("checkout-" + s.instanceID + "-" + req.OrderID)
	session, err := s.client.V1CheckoutSessions.Create(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("stripe hosted create session: %w", err)
	}
	if !ValidStripeCheckoutURL(session.URL) || !strings.HasPrefix(session.ID, "cs_") || session.ExpiresAt == 0 {
		return nil, fmt.Errorf("stripe hosted invalid session response")
	}
	r, err := s.sessionResult(session)
	if err != nil {
		return nil, err
	}
	if r.Metadata["amount_minor"] != strconv.FormatInt(amount, 10) || r.Metadata["order_id"] != req.OrderID || r.Metadata["account_id"] != req.AccountID {
		return nil, fmt.Errorf("stripe hosted created session does not match order")
	}
	return &payment.CreatePaymentResponse{TradeNo: session.ID, PayURL: session.URL, Currency: s.config["currency"], ExpiresAt: session.ExpiresAt}, nil
}
func (s *StripeHosted) sessionResult(cs *stripe.CheckoutSession) (*payment.QueryOrderResponse, error) {
	if cs.Mode != "payment" || cs.Livemode != s.LiveMode() || cs.Metadata["providerKey"] != payment.TypeStripeHosted || cs.Metadata["providerInstanceId"] != s.instanceID || cs.Metadata["orderId"] == "" || cs.ClientReferenceID != cs.Metadata["orderId"] || cs.Metadata["accountId"] == "" {
		return nil, fmt.Errorf("stripe hosted session identity mismatch")
	}
	currency := strings.ToUpper(string(cs.Currency))
	if currency != s.config["currency"] || cs.AmountTotal <= 0 {
		return nil, fmt.Errorf("stripe hosted amount or currency mismatch")
	}
	status := payment.ProviderStatusPending
	if cs.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
		status = payment.ProviderStatusPaid
	} else if cs.Status == stripe.CheckoutSessionStatusComplete {
		status = payment.ProviderStatusProcessing
		if cs.PaymentIntent != nil && (cs.PaymentIntent.Status == stripe.PaymentIntentStatusRequiresPaymentMethod || cs.PaymentIntent.Status == stripe.PaymentIntentStatusCanceled) {
			status = payment.ProviderStatusFailed
		}
	} else if cs.Status == stripe.CheckoutSessionStatusExpired {
		status = payment.ProviderStatusExpired
	}
	return &payment.QueryOrderResponse{TradeNo: cs.ID, PayURL: cs.URL, ExpiresAt: cs.ExpiresAt, Status: status, Amount: payment.MinorUnitToAmount(cs.AmountTotal, currency), Metadata: map[string]string{
		"currency": currency, "amount_minor": strconv.FormatInt(cs.AmountTotal, 10), "order_id": cs.Metadata["orderId"], "instance_id": s.instanceID, "account_id": cs.Metadata["accountId"], "livemode": strconv.FormatBool(cs.Livemode),
	}}, nil
}
func (s *StripeHosted) getSession(ctx context.Context, id string) (*stripe.CheckoutSession, error) {
	if !strings.HasPrefix(id, "cs_") {
		return nil, fmt.Errorf("stripe hosted requires a Checkout Session ID")
	}
	cs, err := s.client.V1CheckoutSessions.Retrieve(ctx, id, &stripe.CheckoutSessionRetrieveParams{Expand: []*string{stripe.String("payment_intent")}})
	if err != nil {
		return nil, err
	}
	if cs.ID != id {
		return nil, fmt.Errorf("stripe hosted queried session mismatch")
	}
	return cs, nil
}
func (s *StripeHosted) QueryOrder(ctx context.Context, id string) (*payment.QueryOrderResponse, error) {
	cs, err := s.getSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.sessionResult(cs)
}
func (s *StripeHosted) VerifyNotification(ctx context.Context, body string, headers map[string]string) (*payment.PaymentNotification, error) {
	event, err := webhook.ConstructEvent([]byte(body), headers["stripe-signature"], s.config["webhookSecret"])
	if err != nil {
		return nil, err
	}
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed", "checkout.session.expired":
	default:
		return nil, nil
	}
	if event.Livemode != s.LiveMode() {
		return nil, fmt.Errorf("stripe hosted event mode mismatch")
	}
	var payload stripe.CheckoutSession
	if err = json.Unmarshal(event.Data.Raw, &payload); err != nil {
		return nil, err
	}
	if _, err = s.sessionResult(&payload); err != nil {
		return nil, err
	}
	// Re-fetch the canonical object: delayed/duplicate events cannot roll state back.
	cs, err := s.getSession(ctx, payload.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHostedWebhookRetry, err)
	}
	r, err := s.sessionResult(cs)
	if err != nil {
		return nil, err
	}
	if event.Account != "" && event.Account != r.Metadata["account_id"] {
		return nil, fmt.Errorf("stripe hosted event account mismatch")
	}
	if r.Status == payment.ProviderStatusPaid {
		r.Status = payment.ProviderStatusSuccess
	} else if event.Type == "checkout.session.async_payment_failed" && cs.PaymentIntent != nil && (cs.PaymentIntent.Status == stripe.PaymentIntentStatusRequiresPaymentMethod || cs.PaymentIntent.Status == stripe.PaymentIntentStatusCanceled) {
		r.Status = payment.ProviderStatusFailed
	}
	r.Metadata["event_id"] = event.ID
	return &payment.PaymentNotification{TradeNo: r.TradeNo, OrderID: r.Metadata["order_id"], Amount: r.Amount, Status: r.Status, Metadata: r.Metadata}, nil
}
func (s *StripeHosted) CancelPayment(ctx context.Context, id string) error {
	cs, err := s.getSession(ctx, id)
	if err != nil {
		return err
	}
	if cs.Status == stripe.CheckoutSessionStatusExpired {
		return nil
	}
	if cs.Status != stripe.CheckoutSessionStatusOpen {
		return fmt.Errorf("stripe hosted payment is already submitted")
	}
	_, err = s.client.V1CheckoutSessions.Expire(ctx, id, nil)
	return err
}
func (s *StripeHosted) refundIntent(ctx context.Context, id, orderID string) (string, error) {
	cs, err := s.getSession(ctx, id)
	if err != nil {
		return "", err
	}
	r, err := s.sessionResult(cs)
	if err != nil {
		return "", err
	}
	if r.Status != payment.ProviderStatusPaid || cs.Metadata["orderId"] != orderID || cs.PaymentIntent == nil || cs.PaymentIntent.ID == "" {
		return "", fmt.Errorf("stripe hosted refund requires original paid session")
	}
	return cs.PaymentIntent.ID, nil
}
func (s *StripeHosted) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	pi, err := s.refundIntent(ctx, req.TradeNo, req.OrderID)
	if err != nil {
		return nil, err
	}
	amount, err := payment.AmountToMinorUnit(req.Amount, s.config["currency"])
	if err != nil || amount <= 0 {
		return nil, fmt.Errorf("stripe hosted invalid refund amount")
	}
	p := &stripe.RefundCreateParams{PaymentIntent: stripe.String(pi), Amount: stripe.Int64(amount), Metadata: map[string]string{"orderId": req.OrderID, "sessionId": req.TradeNo}}
	p.SetIdempotencyKey(fmt.Sprintf("checkout-refund-%s-%s-%d", s.instanceID, req.OrderID, amount))
	r, err := s.client.V1Refunds.Create(ctx, p)
	if err != nil {
		var stripeErr *stripe.Error
		if errors.As(err, &stripeErr) && stripeErr.HTTPStatusCode >= 400 && stripeErr.HTTPStatusCode < 500 && stripeErr.HTTPStatusCode != 409 && stripeErr.HTTPStatusCode != 429 {
			return nil, err
		}
		return &payment.RefundResponse{Status: payment.ProviderStatusPending}, err
	}
	if r.ID == "" || r.PaymentIntent == nil || r.PaymentIntent.ID != pi || r.Amount != amount || r.Metadata["sessionId"] != req.TradeNo {
		return &payment.RefundResponse{Status: payment.ProviderStatusPending}, fmt.Errorf("stripe hosted refund response mismatch")
	}
	return &payment.RefundResponse{RefundID: r.ID, Status: stripeRefundProviderStatus(r.Status)}, nil
}
func (s *StripeHosted) QueryRefund(ctx context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	pi, err := s.refundIntent(ctx, req.TradeNo, req.OrderID)
	if err != nil {
		return nil, err
	}
	amount, err := payment.AmountToMinorUnit(req.Amount, s.config["currency"])
	if err != nil {
		return nil, err
	}
	var r *stripe.Refund
	if req.RefundID != "" {
		r, err = s.client.V1Refunds.Retrieve(ctx, req.RefundID, nil)
	} else {
		p := &stripe.RefundListParams{PaymentIntent: stripe.String(pi)}
		p.Limit = stripe.Int64(100)
		list := s.client.V1Refunds.List(ctx, p)
		for candidate, queryErr := range list.All(ctx) {
			if queryErr != nil {
				return nil, queryErr
			}
			if candidate.Metadata["orderId"] == req.OrderID && candidate.Metadata["sessionId"] == req.TradeNo && candidate.Amount == amount {
				r = candidate
				break
			}
		}
		err = list.Err()
	}
	if err != nil {
		return nil, err
	}
	if r == nil {
		if req.RetryUntil > time.Now().Unix() {
			return s.Refund(ctx, payment.RefundRequest{TradeNo: req.TradeNo, OrderID: req.OrderID, Amount: req.Amount})
		}
		return &payment.RefundResponse{Status: payment.ProviderStatusPending}, nil
	}
	if r.PaymentIntent == nil || r.PaymentIntent.ID != pi || r.Amount != amount || r.Metadata["sessionId"] != req.TradeNo {
		return nil, fmt.Errorf("stripe hosted refund identity mismatch")
	}
	return &payment.RefundResponse{RefundID: r.ID, Status: stripeRefundProviderStatus(r.Status)}, nil
}
