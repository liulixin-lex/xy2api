package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/paymentorder"
	"github.com/liulixin-lex/xy2api/internal/payment"
	"github.com/liulixin-lex/xy2api/internal/payment/provider"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

var hostedRequestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func hostedRequestFingerprint(req CreateOrderRequest) string {
	data, _ := json.Marshal(struct {
		Amount    float64
		PlanID    int64
		OrderType string
	}{req.Amount, req.PlanID, req.OrderType})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func (s *PaymentService) hostedRequestOrder(ctx context.Context, req CreateOrderRequest) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Query().Where(paymentorder.UserIDEQ(req.UserID), paymentorder.IdempotencyKeyEQ(req.IdempotencyKey)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if o.PaymentType != payment.TypeStripeHosted || psSnapshotStringValue(o.ProviderSnapshot["request_fingerprint"]) != hostedRequestFingerprint(req) {
		return nil, infraerrors.Conflict("IDEMPOTENCY_CONFLICT", "payment request key was already used for different parameters")
	}
	return o, nil
}
func (s *PaymentService) createHostedOrder(ctx context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	if !hostedRequestKeyPattern.MatchString(req.IdempotencyKey) {
		return nil, infraerrors.BadRequest("INVALID_IDEMPOTENCY_KEY", "stripe hosted requires an Idempotency-Key of 16–128 letters, digits, underscores or hyphens")
	}
	if req.OrderType == "" {
		req.OrderType = payment.OrderTypeBalance
	}
	if req.OrderType != payment.OrderTypeBalance && req.OrderType != payment.OrderTypeSubscription {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "unsupported order type")
	}
	user, err := s.userRepo.GetByID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	if user.Status != payment.EntityStatusActive {
		return nil, infraerrors.Forbidden("USER_INACTIVE", "user account is disabled")
	}
	previous, err := s.hostedRequestOrder(ctx, req)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		return s.resumeHostedCreation(ctx, previous)
	}
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || !psSliceContains(cfg.EnabledTypes, payment.TypeStripeHosted) {
		return nil, infraerrors.Forbidden("PAYMENT_DISABLED", "stripe hosted is disabled")
	}
	plan, err := s.validateOrderInput(ctx, req, cfg)
	if err != nil {
		return nil, err
	}
	if err = s.checkCancelRateLimit(ctx, req.UserID, cfg); err != nil {
		return nil, err
	}
	limitAmount := req.Amount
	var orderAmount float64
	if plan != nil {
		limitAmount = plan.Price
		orderAmount = plan.Price
	} else {
		orderAmount = calculateCreditedBalance(req.Amount, cfg.BalanceRechargeMultiplier)
	}
	currency, err := s.configService.ValidateMethodCurrencyConsistency(ctx, payment.TypeStripeHosted)
	if err != nil {
		return nil, err
	}
	amountStr, payAmount, err := calculateCreateOrderPayAmountForOrderType(limitAmount, cfg.RechargeFeeRate, currency, req.OrderType, cfg.SubscriptionUSDToCNYRate)
	if err != nil {
		return nil, err
	}
	sel, err := s.selectCreateOrderInstance(ctx, req, cfg, payAmount)
	if err != nil {
		return nil, err
	}
	if sel == nil || sel.ProviderKey != payment.TypeStripeHosted {
		return nil, infraerrors.BadRequest("INVALID_PROVIDER", "stripe hosted requires its own provider")
	}
	if err = s.validateSelectedCreateOrderInstance(ctx, req, sel); err != nil {
		return nil, err
	}
	if paymentProviderConfigCurrency(sel.ProviderKey, sel.Config) != currency {
		return nil, infraerrors.BadRequest("CURRENCY_MISMATCH", "provider currency changed")
	}
	if err = validateSelectedCreateOrderAmountCurrency(amountStr, sel); err != nil {
		return nil, err
	}
	prov, err := provider.NewStripeHosted(sel.InstanceID, sel.Config)
	if err != nil {
		return nil, err
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	account, err := prov.AccountID(deadlineCtx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_UNAVAILABLE", "cannot verify Stripe account")
	}
	origin, err := s.configService.settingRepo.GetValue(ctx, SettingKeyFrontendURL)
	if err != nil {
		return nil, infraerrors.BadRequest("HOSTED_ORIGIN_REQUIRED", "configure the public frontend URL before enabling hosted checkout")
	}
	returnURL, err := hostedReturnURL(origin, prov.LiveMode())
	if err != nil {
		return nil, err
	}
	req.HostedSnapshot = map[string]any{"request_fingerprint": hostedRequestFingerprint(req), "account_id": account, "livemode": strconv.FormatBool(prov.LiveMode()), "hosted_amount": amountStr, "hosted_limit_amount": strconv.FormatFloat(limitAmount, 'f', -1, 64), "hosted_subject": s.buildPaymentSubject(plan, limitAmount, cfg, sel), "hosted_return_url": returnURL}
	sel.PaymentMode = "redirect"
	order, err := s.createOrderInTx(ctx, req, user, plan, cfg, orderAmount, limitAmount, cfg.RechargeFeeRate, payAmount, sel)
	if err != nil {
		if dbent.IsConstraintError(err) {
			if previous, lookupErr := s.hostedRequestOrder(ctx, req); lookupErr == nil && previous != nil {
				return s.resumeHostedCreation(ctx, previous)
			}
		}
		return nil, err
	}
	return s.resumeHostedCreation(ctx, order)
}
func hostedReturnURL(origin string, live bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && (u.Scheme != "http" || live || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1"))) {
		return "", infraerrors.BadRequest("INVALID_HOSTED_ORIGIN", "hosted checkout requires a trusted HTTPS frontend URL (localhost HTTP is allowed in test mode)")
	}
	u.Path = "/payment/result"
	return u.String(), nil
}

// Both the user and selected instance rows are locked by createOrderInTx.
// Reserve pending payments as well as settled funds before admitting a new order.
func checkHostedCapacity(ctx context.Context, tx *dbent.Tx, inst *dbent.PaymentProviderInstance, userID int64, payAmount, limitAmount, dailyLimit float64) error {
	var limits payment.InstanceLimits
	if inst.Limits != "" {
		if err := json.Unmarshal([]byte(inst.Limits), &limits); err != nil {
			return err
		}
	}
	limit := limits[payment.TypeStripeHosted]
	if (limit.SingleMin > 0 && payAmount < limit.SingleMin) || (limit.SingleMax > 0 && payAmount > limit.SingleMax) {
		return infraerrors.TooManyRequests("NO_AVAILABLE_INSTANCE", "payment exceeds provider limits")
	}
	today := psStartOfDayUTC(time.Now())
	if limit.DailyLimit > 0 {
		orders, err := tx.PaymentOrder.Query().Where(paymentorder.ProviderInstanceIDEQ(strconv.FormatInt(inst.ID, 10)), paymentorder.CreatedAtGTE(today), paymentorder.StatusIn(OrderStatusPending, OrderStatusProcessing, OrderStatusPaid, OrderStatusRecharging, OrderStatusCompleted)).All(ctx)
		if err != nil {
			return err
		}
		used := payAmount
		for _, o := range orders {
			used += o.PayAmount
		}
		if used > limit.DailyLimit {
			return infraerrors.TooManyRequests("NO_AVAILABLE_INSTANCE", "provider daily limit exceeded")
		}
	}
	if dailyLimit > 0 {
		orders, err := tx.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID), paymentorder.Or(
			paymentorder.And(paymentorder.StatusIn(OrderStatusPending, OrderStatusProcessing), paymentorder.CreatedAtGTE(today)),
			paymentorder.And(paymentorder.StatusIn(OrderStatusPaid, OrderStatusRecharging, OrderStatusCompleted), paymentorder.PaidAtGTE(today)),
		)).All(ctx)
		if err != nil {
			return err
		}
		used := limitAmount
		for _, o := range orders {
			if raw := psSnapshotStringValue(o.ProviderSnapshot["hosted_limit_amount"]); raw != "" {
				amount, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					return err
				}
				used += amount
			} else if o.OrderType == payment.OrderTypeBalance {
				used += o.PayAmount
			} else {
				used += o.Amount
			}
		}
		if used > dailyLimit {
			return infraerrors.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily_limit_exceeded")
		}
	}
	return nil
}

// Frozen parameters are committed before the first Stripe request. Concurrent
// attempts may call Stripe with the same key; Stripe serializes creation for us.
func (s *PaymentService) resumeHostedCreation(ctx context.Context, o *dbent.PaymentOrder) (*CreateOrderResponse, error) {
	if o.PaymentTradeNo != "" {
		// Always re-fetch before returning a bearer checkout URL to the browser.
		if _, err := s.reconcileHostedOrder(ctx, o); err != nil {
			return nil, err
		}
		refreshed, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		return hostedOrderResponse(refreshed), nil
	}
	if o.Status != OrderStatusPending || time.Since(o.CreatedAt) > 23*time.Hour || !time.Now().Before(o.ExpiresAt) {
		return nil, infraerrors.Conflict("HOSTED_RECOVERY_EXPIRED", "checkout creation recovery expired; review this order before making another payment")
	}
	p, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return nil, err
	}
	prov, ok := p.(*provider.StripeHosted)
	if !ok {
		return nil, fmt.Errorf("hosted provider mismatch")
	}
	rawURL := psSnapshotStringValue(o.ProviderSnapshot["hosted_return_url"])
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("order_id", strconv.FormatInt(o.ID, 10))
	q.Set("out_trade_no", o.OutTradeNo)
	u.RawQuery = q.Encode()
	expiry, err := strconv.ParseInt(psSnapshotStringValue(o.ProviderSnapshot["hosted_expires_at"]), 10, 64)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := prov.CreatePayment(callCtx, payment.CreatePaymentRequest{OrderID: o.OutTradeNo, Amount: psSnapshotStringValue(o.ProviderSnapshot["hosted_amount"]), Subject: psSnapshotStringValue(o.ProviderSnapshot["hosted_subject"]), ReturnURL: u.String(), AccountID: psSnapshotStringValue(o.ProviderSnapshot["account_id"]), ExpiresAt: expiry})
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "HOSTED_CREATE_UNCERTAIN", "system", map[string]any{"retry_same_request": true})
		return nil, infraerrors.ServiceUnavailable("HOSTED_CREATE_RETRY", "checkout is not confirmed; retry the same payment request").WithMetadata(map[string]string{"order_id": strconv.FormatInt(o.ID, 10)})
	}
	// A callback can arrive before this response. Do not overwrite its state.
	updated, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.Or(paymentorder.PaymentTradeNoEQ(""), paymentorder.PaymentTradeNoEQ(result.TradeNo))).SetPaymentTradeNo(result.TradeNo).SetPayURL(result.PayURL).SetExpiresAt(time.Unix(result.ExpiresAt, 0)).Save(ctx)
	if err != nil {
		return nil, err
	}
	if updated != 1 {
		return nil, fmt.Errorf("hosted session binding conflict")
	}
	o, err = s.entClient.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	s.writeAuditLog(ctx, o.ID, "HOSTED_SESSION_CREATED", "system", map[string]any{"session_id": result.TradeNo})
	return hostedOrderResponse(o), nil
}
func hostedOrderResponse(o *dbent.PaymentOrder) *CreateOrderResponse {
	r := &CreateOrderResponse{OrderID: o.ID, OutTradeNo: o.OutTradeNo, Amount: o.Amount, PayAmount: o.PayAmount, FeeRate: o.FeeRate, Status: o.Status, PaymentType: payment.TypeStripeHosted, PaymentMode: "redirect", Currency: PaymentOrderCurrency(o), ExpiresAt: o.ExpiresAt, ResultType: payment.CreatePaymentResultOrderCreated}
	if o.Status == OrderStatusPending && time.Now().Before(o.ExpiresAt) && o.PayURL != nil && provider.ValidStripeCheckoutURL(*o.PayURL) {
		r.PayURL = *o.PayURL
	}
	return r
}
func validateHostedNotification(o *dbent.PaymentOrder, n *payment.PaymentNotification) error {
	if o.PaymentType != payment.TypeStripeHosted || psStringValue(o.ProviderKey) != payment.TypeStripeHosted || n.OrderID != o.OutTradeNo || !strings.HasPrefix(n.TradeNo, "cs_") {
		return fmt.Errorf("hosted order identity mismatch")
	}
	if o.PaymentTradeNo != "" && o.PaymentTradeNo != n.TradeNo {
		return fmt.Errorf("hosted session mismatch")
	}
	for key, snapshotKey := range map[string]string{"instance_id": "provider_instance_id", "account_id": "account_id", "livemode": "livemode", "currency": "currency"} {
		expected := psSnapshotStringValue(o.ProviderSnapshot[snapshotKey])
		if expected == "" || n.Metadata[key] != expected {
			return fmt.Errorf("hosted %s mismatch", key)
		}
	}
	expected, err := payment.AmountToMinorUnit(psSnapshotStringValue(o.ProviderSnapshot["hosted_amount"]), PaymentOrderCurrency(o))
	if err != nil {
		return err
	}
	if n.Metadata["amount_minor"] != strconv.FormatInt(expected, 10) {
		return fmt.Errorf("hosted amount mismatch")
	}
	return nil
}
func (s *PaymentService) handleHostedNotification(ctx context.Context, n *payment.PaymentNotification) error {
	o, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNoEQ(n.OrderID)).Only(ctx)
	if err != nil {
		return err
	}
	if err = validateHostedNotification(o, n); err != nil {
		return err
	}
	// Bind early callbacks atomically to the original session.
	if o.PaymentTradeNo == "" {
		changed, e := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.PaymentTradeNoEQ("")).SetPaymentTradeNo(n.TradeNo).Save(ctx)
		if e != nil {
			return e
		}
		if changed == 0 {
			current, e := s.entClient.PaymentOrder.Get(ctx, o.ID)
			if e != nil {
				return e
			}
			if current.PaymentTradeNo != n.TradeNo {
				return fmt.Errorf("hosted session binding conflict")
			}
		}
		o.PaymentTradeNo = n.TradeNo
	}
	switch n.Status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusPaid:
		return s.toPaid(ctx, o, n.TradeNo, n.Amount, payment.TypeStripeHosted)
	case payment.ProviderStatusProcessing:
		_, err = s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(OrderStatusPending)).SetStatus(OrderStatusProcessing).Save(ctx)
	case payment.ProviderStatusFailed, payment.ProviderStatusExpired:
		target := OrderStatusFailed
		if n.Status == payment.ProviderStatusExpired {
			target = OrderStatusExpired
		}
		_, err = s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.PaidAtIsNil(), paymentorder.StatusIn(OrderStatusPending, OrderStatusProcessing)).SetStatus(target).Save(ctx)
	}
	return err
}
func (s *PaymentService) HostedWebhookProvider(ctx context.Context, id int64) (payment.Provider, error) {
	inst, err := s.entClient.PaymentProviderInstance.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if inst.ProviderKey != payment.TypeStripeHosted {
		return nil, fmt.Errorf("incorrect webhook provider")
	}
	return s.createProviderFromInstance(ctx, inst)
}
func (s *PaymentService) reconcileHostedOrder(ctx context.Context, o *dbent.PaymentOrder) (string, error) {
	if o.PaymentTradeNo == "" {
		return "", nil
	}
	p, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := p.QueryOrder(callCtx, o.PaymentTradeNo)
	if err != nil {
		return "", err
	}
	n := &payment.PaymentNotification{OrderID: o.OutTradeNo, TradeNo: r.TradeNo, Amount: r.Amount, Status: r.Status, Metadata: r.Metadata}
	if err = s.handleHostedNotification(ctx, n); err != nil {
		return "", err
	}
	if r.Status == payment.ProviderStatusPending && provider.ValidStripeCheckoutURL(r.PayURL) && r.ExpiresAt > 0 {
		_, err = s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.PaymentTradeNoEQ(r.TradeNo)).SetPayURL(r.PayURL).SetExpiresAt(time.Unix(r.ExpiresAt, 0)).Save(ctx)
		if err != nil {
			return "", err
		}
	}
	return r.Status, nil
}
func (s *PaymentService) cancelHostedOrder(ctx context.Context, o *dbent.PaymentOrder, finalStatus, operator string) (string, error) {
	if o.PaymentTradeNo == "" {
		return "", infraerrors.Conflict("HOSTED_CREATE_RETRY", "checkout creation is uncertain; recover the original request first")
	}
	p, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return "", err
	}
	r, err := s.reconcileHostedOrder(ctx, o)
	if err != nil {
		return "", err
	}
	if r == payment.ProviderStatusPaid {
		return checkPaidResultAlreadyPaid, nil
	}
	if r == payment.ProviderStatusProcessing {
		return "", infraerrors.Conflict("PAYMENT_PROCESSING", "payment has already been submitted")
	}
	if r != payment.ProviderStatusExpired {
		cp, ok := p.(payment.CancelableProvider)
		if !ok {
			return "", fmt.Errorf("provider cannot expire session")
		}
		if err = cp.CancelPayment(ctx, o.PaymentTradeNo); err != nil {
			r, recheckErr := s.reconcileHostedOrder(ctx, o)
			if recheckErr == nil && r == payment.ProviderStatusPaid {
				return checkPaidResultAlreadyPaid, nil
			}
			return "", err
		}
	}
	changed, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), paymentorder.StatusIn(OrderStatusPending, OrderStatusExpired), paymentorder.PaidAtIsNil()).SetStatus(finalStatus).Save(ctx)
	if err != nil {
		return "", err
	}
	if changed == 0 {
		current, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if err != nil {
			return "", err
		}
		if current.PaidAt != nil {
			return checkPaidResultAlreadyPaid, nil
		}
		if current.Status != finalStatus {
			return "", infraerrors.Conflict("PAYMENT_PROCESSING", "payment state changed while cancelling")
		}
	}
	action := "ORDER_CANCELLED"
	if finalStatus == OrderStatusExpired {
		action = "ORDER_EXPIRED"
	}
	s.writeAuditLog(ctx, o.ID, action, operator, map[string]any{"session_id": o.PaymentTradeNo, "status": finalStatus})
	return checkPaidResultCancelled, nil
}

// A cursor over IDs, rather than always the first batch, prevents a slow bank
// debit from starving later orders. Existing singleton scheduling bounds calls.
func (s *PaymentService) reconcileHostedOrders(ctx context.Context) (int, error) {
	s.hostedReconcileMu.Lock()
	defer s.hostedReconcileMu.Unlock()
	recovered := 0
	{
		orders, err := s.entClient.PaymentOrder.Query().Where(paymentorder.IDGT(s.hostedReconcileCursor), paymentorder.PaymentTypeEQ(payment.TypeStripeHosted), paymentorder.Or(paymentorder.StatusIn(OrderStatusPending, OrderStatusProcessing, OrderStatusPaid, OrderStatusRecharging), paymentorder.And(paymentorder.StatusEQ(OrderStatusFailed), paymentorder.PaidAtNotNil()))).Order(dbent.Asc(paymentorder.FieldID)).Limit(20).All(ctx)
		if err != nil {
			return recovered, err
		}
		if len(orders) == 0 {
			s.hostedReconcileCursor = 0
			return recovered, nil
		}
		for _, o := range orders {
			if err = ctx.Err(); err != nil {
				return recovered, err
			}
			s.hostedReconcileCursor = o.ID
			if o.PaymentTradeNo == "" {
				if time.Since(o.CreatedAt) < 23*time.Hour && time.Now().Before(o.ExpiresAt) {
					_, err = s.resumeHostedCreation(ctx, o)
				}
			} else {
				var status string
				status, err = s.reconcileHostedOrder(ctx, o)
				if status == payment.ProviderStatusPaid {
					recovered++
				}
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				s.writeAuditLog(ctx, o.ID, "HOSTED_RECONCILE_RETRY", "system", map[string]any{"retry": true})
			}
		}
		if len(orders) < 20 {
			s.hostedReconcileCursor = 0
		}
	}
	return recovered, nil
}

func (s *PaymentService) ResumeStripeHostedOrder(ctx context.Context, id, userID int64) (*CreateOrderResponse, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, id)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != userID {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission for this order")
	}
	if o.PaymentType != payment.TypeStripeHosted {
		return nil, infraerrors.BadRequest("INVALID_PROVIDER", "not a hosted checkout order")
	}
	return s.resumeHostedCreation(ctx, o)
}
