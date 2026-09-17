//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/payment"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
)

func TestStripeHostedRefundHoldsAndRestoresExactlyOnce(t *testing.T) {
	if os.Getenv("STRIPE_HOSTED_TEST_DSN") == "" {
		t.Skip("requires PostgreSQL refund transactions")
	}
	for _, subscription := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(strconv.FormatBool(subscription)+"/failed="+strconv.FormatBool(failed), func(t *testing.T) {
				ctx := context.Background()
				s, o, _ := hostedServiceFixture(t)
				_, err := s.entClient.User.UpdateOneID(o.UserID).SetBalance(100).Save(ctx)
				require.NoError(t, err)
				o, err = s.entClient.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusCompleted).SetPaidAt(time.Now()).Save(ctx)
				require.NoError(t, err)
				key := []byte("0123456789abcdef0123456789abcdef")
				cfg := NewPaymentConfigService(s.entClient, nil, key)
				inst, err := cfg.CreateProviderInstance(ctx, CreateProviderInstanceRequest{Name: "Hosted", ProviderKey: "stripe_hosted", Enabled: true, RefundEnabled: true, Config: map[string]string{"secretKey": "sk_test_fixture", "webhookSecret": "whsec_fixture", "currency": "CNY"}})
				require.NoError(t, err)
				require.Equal(t, int64(1), inst.ID)
				s.loadBalancer = payment.NewDefaultLoadBalancer(s.entClient, key)
				p := &RefundPlan{OrderID: o.ID, Order: o, RefundAmount: 40, GatewayAmount: 40, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 40, DeductBalance: true}
				var subID int64
				var before time.Time
				if subscription {
					g, err := s.entClient.Group.Create().SetName("Refund Group").Save(ctx)
					require.NoError(t, err)
					sub, err := s.entClient.UserSubscription.Create().SetUserID(o.UserID).SetGroupID(g.ID).SetStartsAt(time.Now()).SetExpiresAt(time.Now().AddDate(0, 0, 15)).Save(ctx)
					require.NoError(t, err)
					subID = sub.ID
					before = sub.ExpiresAt
					o, err = s.entClient.PaymentOrder.UpdateOneID(o.ID).SetOrderType("subscription").SetSubscriptionGroupID(g.ID).SetSubscriptionDays(30).Save(ctx)
					require.NoError(t, err)
					p.Order = o
					p.DeductionType = payment.DeductionTypeSubscription
					p.BalanceToDeduct = 0
					p.SubscriptionID = sub.ID
					p.SubDaysToDeduct = 30
				}
				cs := &stripe.CheckoutSession{ID: o.PaymentTradeNo, Mode: "payment", Status: "complete", PaymentStatus: "paid", Currency: "cny", AmountTotal: 8000, ClientReferenceID: o.OutTradeNo, Metadata: map[string]string{"orderId": o.OutTradeNo, "providerKey": "stripe_hosted", "providerInstanceId": "1", "accountId": "acct_test"}, PaymentIntent: &stripe.PaymentIntent{ID: "pi_hold"}}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
						_ = json.NewEncoder(w).Encode(cs)
						return
					}
					if r.Method == http.MethodPost {
						w.WriteHeader(503)
						_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"uncertain"}}`))
						return
					}
					status := "succeeded"
					if failed {
						status = "failed"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": "re_hold", "status": status, "amount": 4000, "payment_intent": "pi_hold", "metadata": map[string]string{"orderId": o.OutTradeNo, "sessionId": o.PaymentTradeNo}}}, "has_more": false})
				}))
				defer server.Close()
				previous := stripe.GetBackend(stripe.APIBackend)
				stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), MaxNetworkRetries: stripe.Int64(0)}))
				defer stripe.SetBackend(stripe.APIBackend, previous)
				r, err := s.ExecuteRefund(ctx, p)
				require.NoError(t, err)
				require.False(t, r.Success)
				pending, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusRefundPending, pending.Status)
				user, err := s.entClient.User.Get(ctx, o.UserID)
				require.NoError(t, err)
				if subscription {
					sub, err := s.entClient.UserSubscription.Get(ctx, subID)
					require.NoError(t, err)
					require.Equal(t, SubscriptionStatusExpired, sub.Status)
					require.Nil(t, sub.DeletedAt)
				} else {
					require.Equal(t, 60.0, user.Balance)
				}
				r, err = s.QueryAndFinalizeRefund(ctx, o.ID)
				require.NoError(t, err)
				require.Equal(t, !failed, r.Success)
				_, err = s.QueryAndFinalizeRefund(ctx, o.ID)
				require.Error(t, err)
				user, err = s.entClient.User.Get(ctx, o.UserID)
				require.NoError(t, err)
				if subscription {
					sub, err := s.entClient.UserSubscription.Get(ctx, subID)
					require.NoError(t, err)
					if failed {
						require.WithinDuration(t, before, sub.ExpiresAt, time.Millisecond)
						require.Equal(t, SubscriptionStatusActive, sub.Status)
					} else {
						require.Equal(t, SubscriptionStatusExpired, sub.Status)
					}
				} else if failed {
					require.Equal(t, 100.0, user.Balance)
				} else {
					require.Equal(t, 60.0, user.Balance)
				}
			})
		}
	}
}
