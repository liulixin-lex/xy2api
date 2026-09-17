package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/paymentorder"
	userent "github.com/liulixin-lex/xy2api/ent/user"
	"github.com/liulixin-lex/xy2api/ent/usersubscription"
	"github.com/liulixin-lex/xy2api/internal/payment"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// Persist the refund request and its holds in the same transaction. A crash or
// an ambiguous Stripe response can then be recovered without deducting twice.
type hostedRefundJournal struct {
	Amount                  float64 `json:"amount"`
	GatewayAmount           string  `json:"gateway_amount"`
	StartedAt               int64   `json:"started_at"`
	RefundID                string  `json:"refund_id"`
	BalanceHeld             float64 `json:"balance_held"`
	SubscriptionID          int64   `json:"subscription_id"`
	SubscriptionHeldSeconds float64 `json:"subscription_held_seconds"`
	SubscriptionDays        int     `json:"subscription_days"`
}

func readHostedRefund(o *dbent.PaymentOrder) (*hostedRefundJournal, error) {
	raw, ok := o.ProviderSnapshot["hosted_refund"]
	if !ok {
		return nil, fmt.Errorf("hosted refund journal missing")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var journal hostedRefundJournal
	if err = json.Unmarshal(data, &journal); err != nil {
		return nil, err
	}
	if journal.StartedAt == 0 || journal.GatewayAmount == "" {
		return nil, fmt.Errorf("hosted refund journal invalid")
	}
	return &journal, nil
}

func hostedRefundSnapshot(o *dbent.PaymentOrder, journal *hostedRefundJournal) map[string]any {
	snapshot := make(map[string]any, len(o.ProviderSnapshot)+1)
	for k, v := range o.ProviderSnapshot {
		snapshot[k] = v
	}
	snapshot["hosted_refund"] = journal
	return snapshot
}

func (s *PaymentService) executeHostedRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	if p.Order.Status == OrderStatusRefundPending {
		return s.queryHostedRefund(ctx, p.Order)
	}
	if p.Order.PaymentTradeNo == "" || p.Order.PaidAt == nil {
		return nil, fmt.Errorf("hosted refund requires verified payment")
	}
	if _, exists := p.Order.ProviderSnapshot["hosted_refund"]; exists {
		return nil, infraerrors.Conflict("REFUND_ALREADY_ATTEMPTED", "the original refund attempt must be reviewed before another refund")
	}
	prov, err := s.getRefundProvider(ctx, p.Order)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := prov.QueryOrder(callCtx, p.Order.PaymentTradeNo)
	if err != nil {
		return nil, err
	}
	if r.Status != payment.ProviderStatusPaid {
		return nil, fmt.Errorf("hosted session is not paid")
	}
	if err = validateHostedNotification(p.Order, &payment.PaymentNotification{OrderID: p.Order.OutTradeNo, TradeNo: r.TradeNo, Metadata: r.Metadata}); err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	o, err := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(p.OrderID)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	if (o.Status != OrderStatusCompleted && o.Status != OrderStatusRefundRequested) || o.ProviderSnapshot["hosted_refund"] != nil {
		return nil, infraerrors.Conflict("CONFLICT", "refund state changed")
	}
	j := &hostedRefundJournal{Amount: p.RefundAmount, GatewayAmount: formatGatewayRefundAmount(p.GatewayAmount, o), StartedAt: time.Now().Unix()}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		u, err := tx.User.Query().Where(userent.IDEQ(o.UserID), userent.DeletedAtIsNil()).ForUpdate().Only(ctx)
		if err != nil {
			return nil, err
		}
		if !p.Force && u.Balance < p.BalanceToDeduct {
			return nil, infraerrors.Conflict("BALANCE_NOT_ENOUGH", "balance changed before refund")
		}
		j.BalanceHeld = math.Max(0, math.Min(u.Balance, p.BalanceToDeduct))
		if _, err = tx.User.UpdateOneID(u.ID).AddBalance(-j.BalanceHeld).Save(ctx); err != nil {
			return nil, err
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubscriptionID > 0 && p.SubDaysToDeduct > 0 {
		sub, err := tx.UserSubscription.Query().Where(usersubscription.IDEQ(p.SubscriptionID), usersubscription.DeletedAtIsNil()).ForUpdate().Only(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		next := sub.ExpiresAt.AddDate(0, 0, -p.SubDaysToDeduct)
		if next.Before(now) {
			next = now
		}
		if sub.ExpiresAt.After(next) {
			j.SubscriptionID = sub.ID
			j.SubscriptionHeldSeconds = sub.ExpiresAt.Sub(next).Seconds()
			j.SubscriptionDays = p.SubDaysToDeduct
			update := tx.UserSubscription.UpdateOneID(sub.ID).SetExpiresAt(next)
			if !next.After(now) {
				update.SetStatus(SubscriptionStatusExpired)
			}
			if _, err = update.Save(ctx); err != nil {
				return nil, err
			}
		}
	}
	_, err = tx.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusRefundPending).SetRefundAmount(j.Amount).SetRefundReason(p.Reason).SetForceRefund(p.Force).SetProviderSnapshot(hostedRefundSnapshot(o, j)).Save(ctx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateHostedRefundCaches(o)
	resp, err := prov.Refund(callCtx, payment.RefundRequest{OrderID: o.OutTradeNo, TradeNo: o.PaymentTradeNo, Amount: j.GatewayAmount, Reason: p.Reason})
	if err != nil {
		if resp == nil || resp.Status != payment.ProviderStatusPending {
			return s.finishHostedRefund(ctx, o.ID, &payment.RefundResponse{Status: payment.ProviderStatusFailed})
		}
	}
	return s.finishHostedRefund(ctx, o.ID, resp)
}

func (s *PaymentService) queryHostedRefund(ctx context.Context, o *dbent.PaymentOrder) (*RefundResult, error) {
	j, err := readHostedRefund(o)
	if err != nil {
		return nil, err
	}
	prov, err := s.getRefundProvider(ctx, o)
	if err != nil {
		return nil, err
	}
	query, ok := prov.(payment.RefundQueryProvider)
	if !ok {
		return nil, fmt.Errorf("hosted refund query unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := query.QueryRefund(callCtx, payment.RefundQueryRequest{OrderID: o.OutTradeNo, TradeNo: o.PaymentTradeNo, Amount: j.GatewayAmount, RefundID: j.RefundID, RetryUntil: j.StartedAt + int64((23 * time.Hour).Seconds())})
	if err != nil {
		return nil, err
	}
	return s.finishHostedRefund(ctx, o.ID, resp)
}

func (s *PaymentService) finishHostedRefund(ctx context.Context, id int64, resp *payment.RefundResponse) (*RefundResult, error) {
	if resp == nil {
		return nil, fmt.Errorf("hosted refund response missing")
	}
	if resp.Status != payment.ProviderStatusPending && resp.Status != payment.ProviderStatusSuccess && resp.Status != payment.ProviderStatusRefunded && resp.Status != payment.ProviderStatusFailed {
		return nil, fmt.Errorf("hosted refund status unknown")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	o, err := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	if o.Status != OrderStatusRefundPending {
		if o.Status == OrderStatusRefunded || o.Status == OrderStatusPartiallyRefunded {
			return &RefundResult{Success: true}, nil
		}
		return nil, infraerrors.Conflict("CONFLICT", "refund state changed")
	}
	j, err := readHostedRefund(o)
	if err != nil {
		return nil, err
	}
	if resp.RefundID != "" {
		if j.RefundID != "" && j.RefundID != resp.RefundID {
			return nil, fmt.Errorf("hosted refund identity changed")
		}
		j.RefundID = resp.RefundID
	}
	u := tx.PaymentOrder.UpdateOneID(id).SetProviderSnapshot(hostedRefundSnapshot(o, j))
	result := &RefundResult{Success: false, Warning: "gateway refund is pending confirmation"}
	if resp.Status == payment.ProviderStatusSuccess || resp.Status == payment.ProviderStatusRefunded {
		if j.RefundID == "" {
			return nil, fmt.Errorf("hosted refund success missing id")
		}
		status := OrderStatusRefunded
		if j.Amount < o.Amount {
			status = OrderStatusPartiallyRefunded
		}
		u.SetStatus(status).SetRefundAt(time.Now())
		result = &RefundResult{Success: true, BalanceDeducted: j.BalanceHeld, SubDaysDeducted: j.SubscriptionDays}
	} else if resp.Status == payment.ProviderStatusFailed {
		if j.BalanceHeld > 0 {
			if _, err = tx.User.UpdateOneID(o.UserID).AddBalance(j.BalanceHeld).Save(ctx); err != nil {
				return nil, err
			}
		}
		if j.SubscriptionID > 0 && j.SubscriptionHeldSeconds > 0 {
			sub, err := tx.UserSubscription.Query().Where(usersubscription.IDEQ(j.SubscriptionID), usersubscription.DeletedAtIsNil()).ForUpdate().Only(ctx)
			if err != nil {
				return nil, err
			}
			expires := sub.ExpiresAt.Add(time.Duration(j.SubscriptionHeldSeconds * float64(time.Second)))
			update := tx.UserSubscription.UpdateOneID(sub.ID).SetExpiresAt(expires)
			if expires.After(time.Now()) {
				update.SetStatus(SubscriptionStatusActive)
			}
			if _, err = update.Save(ctx); err != nil {
				return nil, err
			}
		}
		u.SetStatus(OrderStatusRefundFailed).SetFailedAt(time.Now()).SetFailedReason("Stripe refund failed; local hold restored")
		result.Warning = "gateway refund failed; local hold restored"
	}
	if _, err = u.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateHostedRefundCaches(o)
	s.writeAuditLog(ctx, id, "HOSTED_REFUND_"+resp.Status, "admin", map[string]any{"refund_id": j.RefundID, "amount": j.GatewayAmount})
	return result, nil
}

func (s *PaymentService) invalidateHostedRefundCaches(o *dbent.PaymentOrder) {
	if s.redeemService != nil && s.redeemService.billingCacheService != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.redeemService.billingCacheService.InvalidateUserBalance(ctx, o.UserID)
	}
	if s.subscriptionSvc != nil && o.SubscriptionGroupID != nil {
		_ = s.subscriptionSvc.invalidateSubscriptionCaches(o.UserID, *o.SubscriptionGroupID)
	}
}
