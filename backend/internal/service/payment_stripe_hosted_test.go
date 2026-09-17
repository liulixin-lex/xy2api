//go:build unit

package service

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/payment"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func hostedServiceFixture(t *testing.T) (*PaymentService, *dbent.PaymentOrder, *float64) {
	t.Helper()
	ctx := context.Background()
	client := newHostedDatabase(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	o := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now().Add(-24*time.Hour))
	snapshot := map[string]any{"schema_version": 1, "provider_key": "stripe_hosted", "provider_instance_id": "1", "account_id": "acct_test", "livemode": "false", "currency": "CNY", "hosted_amount": "80.00", "request_fingerprint": hostedRequestFingerprint(CreateOrderRequest{Amount: 80, OrderType: "balance"})}
	var err error
	o, err = client.PaymentOrder.UpdateOneID(o.ID).SetPaymentType("stripe_hosted").SetProviderKey("stripe_hosted").SetProviderInstanceID("1").SetProviderSnapshot(snapshot).SetPaymentTradeNo("cs_test_1").SetOrderType("balance").SetIdempotencyKey("request_1234567890").ClearPaidAt().ClearPlanID().ClearSubscriptionGroupID().ClearSubscriptionDays().Save(ctx)
	require.NoError(t, err)
	credited := new(float64)
	users := &mockUserRepo{getByIDUser: &User{ID: o.UserID, Balance: 0, Status: payment.EntityStatusActive}}
	users.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, o.UserID, id)
		*credited += amount
		return nil
	}
	codes := &paymentFulfillmentRedeemRepo{}
	redeem := NewRedeemService(codes, users, nil, &paymentFulfillmentRedeemCacheStub{}, nil, client, nil, nil)
	return &PaymentService{entClient: client, redeemService: redeem, userRepo: users}, o, credited
}
func hostedNotification(o *dbent.PaymentOrder, status string) *payment.PaymentNotification {
	return &payment.PaymentNotification{OrderID: o.OutTradeNo, TradeNo: "cs_test_1", Amount: 80, Status: status, Metadata: map[string]string{"currency": "CNY", "amount_minor": "8000", "instance_id": "1", "account_id": "acct_test", "livemode": "false"}}
}
func TestStripeHostedFulfillmentAndLateEvents(t *testing.T) {
	for _, initial := range []string{OrderStatusPending, OrderStatusExpired, OrderStatusCancelled, OrderStatusFailed} {
		t.Run(initial, func(t *testing.T) {
			s, o, credited := hostedServiceFixture(t)
			ctx := context.Background()
			_, err := s.entClient.PaymentOrder.UpdateOneID(o.ID).SetStatus(initial).SetUpdatedAt(time.Now().Add(-24 * time.Hour)).Save(ctx)
			require.NoError(t, err)
			if initial == OrderStatusPending {
				require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "processing"), "stripe_hosted"))
				current, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusProcessing, current.Status)
				require.Zero(t, *credited)
			}
			n := hostedNotification(o, "success")
			require.NoError(t, s.HandlePaymentNotification(ctx, n, "stripe_hosted"))
			require.NoError(t, s.HandlePaymentNotification(ctx, n, "stripe_hosted"))
			require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "expired"), "stripe_hosted"))
			require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "failed"), "stripe_hosted"))
			current, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, current.Status)
			require.Equal(t, 80.0, *credited)
		})
	}
}
func TestStripeHostedRejectsNotificationTampering(t *testing.T) {
	for _, field := range []string{"currency", "amount_minor", "instance_id", "account_id", "livemode", "session", "order"} {
		t.Run(field, func(t *testing.T) {
			s, o, credited := hostedServiceFixture(t)
			n := hostedNotification(o, "success")
			switch field {
			case "session":
				n.TradeNo = "cs_other"
			case "order":
				n.OrderID = "other"
			default:
				n.Metadata[field] = "invalid"
			}
			require.Error(t, s.HandlePaymentNotification(context.Background(), n, "stripe_hosted"))
			require.Zero(t, *credited)
		})
	}
}
func TestStripeHostedIdempotencyAndOwnership(t *testing.T) {
	s, o, _ := hostedServiceFixture(t)
	ctx := context.Background()
	req := CreateOrderRequest{UserID: o.UserID, IdempotencyKey: "request_1234567890", Amount: 80, OrderType: "balance"}
	found, err := s.hostedRequestOrder(ctx, req)
	require.NoError(t, err)
	require.Equal(t, o.ID, found.ID)
	req.Amount = 81
	_, err = s.hostedRequestOrder(ctx, req)
	require.Error(t, err)
	req.UserID++
	found, err = s.hostedRequestOrder(ctx, req)
	require.NoError(t, err)
	require.Nil(t, found)
	_, err = s.ResumeStripeHostedOrder(ctx, o.ID, o.UserID+1)
	require.Error(t, err)
	_, err = s.VerifyOrderByOutTradeNo(ctx, o.OutTradeNo, o.UserID+1)
	require.Error(t, err)
	for _, key := range []string{"", "short", "<script>"} {
		_, err = s.createHostedOrder(ctx, CreateOrderRequest{UserID: o.UserID, IdempotencyKey: key})
		require.Error(t, err)
	}
}

func TestStripeHostedDatabaseFailureIsRetryable(t *testing.T) {
	s, o, credited := hostedServiceFixture(t)
	require.NoError(t, s.entClient.Close())
	require.Error(t, s.HandlePaymentNotification(context.Background(), hostedNotification(o, "success"), "stripe_hosted"))
	require.Zero(t, *credited)
}
func TestStripeHostedOriginAndEncryptedCredentials(t *testing.T) {
	for _, raw := range []string{"https://evil.test@trusted.test", "https://trusted.test/path", "https://trusted.test?url=x", "http://trusted.test", "//trusted.test"} {
		_, err := hostedReturnURL(raw, false)
		require.Error(t, err)
	}
	_, err := hostedReturnURL("http://localhost:5173", true)
	require.Error(t, err)
	got, err := hostedReturnURL("http://localhost:5173", false)
	require.NoError(t, err)
	require.Equal(t, "http://localhost:5173/payment/result", got)
	cfg := &PaymentConfigService{}
	secrets := map[string]string{"secretKey": "sk_test_private", "webhookSecret": "whsec_private"}
	_, err = cfg.storeProviderConfig("stripe_hosted", secrets)
	require.Error(t, err)
	cfg.encryptionKey = []byte("0123456789abcdef0123456789abcdef")
	encrypted, err := cfg.storeProviderConfig("stripe_hosted", secrets)
	require.NoError(t, err)
	require.NotContains(t, encrypted, "private")
	decrypted, err := cfg.decryptConfig(encrypted)
	require.NoError(t, err)
	require.Equal(t, secrets, decrypted)
	masked, err := cfg.decryptAndMaskConfig("stripe_hosted", encrypted)
	require.NoError(t, err)
	require.NotEqual(t, secrets["secretKey"], masked["secretKey"])
}
func TestStripeHostedDisabledInstanceRetainsHistory(t *testing.T) {
	if os.Getenv("STRIPE_HOSTED_TEST_DSN") == "" {
		t.Skip("requires PostgreSQL provider row locks")
	}
	s, o, _ := hostedServiceFixture(t)
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := NewPaymentConfigService(s.entClient, nil, key)
	inst, err := cfg.CreateProviderInstance(ctx, CreateProviderInstanceRequest{Name: "Hosted", ProviderKey: "stripe_hosted", Config: map[string]string{"secretKey": "sk_test_private", "webhookSecret": "whsec_private"}, Enabled: true})
	require.NoError(t, err)
	_, err = s.entClient.PaymentOrder.UpdateOneID(o.ID).SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).Save(ctx)
	require.NoError(t, err)
	disabled := false
	_, err = cfg.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Enabled: &disabled})
	require.NoError(t, err)
	require.Error(t, cfg.DeleteProviderInstance(ctx, inst.ID))
	_, err = cfg.UpdateProviderInstance(ctx, inst.ID, UpdateProviderInstanceRequest{Config: map[string]string{"secretKey": "sk_test_changed"}})
	require.Error(t, err)
}

func TestStripeHostedConcurrentFulfillment(t *testing.T) {
	if os.Getenv("STRIPE_HOSTED_TEST_DSN") == "" {
		t.Skip("requires PostgreSQL concurrency")
	}
	s, o, credited := hostedServiceFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.HandlePaymentNotification(ctx, hostedNotification(o, "success"), "stripe_hosted")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			require.Equal(t, "CONFLICT", infraerrors.Reason(err))
		}
	}
	require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "success"), "stripe_hosted"))
	current, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status)
	require.Equal(t, 80.0, *credited)
}

func TestStripeHostedSubscriptionDoesNotExtendTwice(t *testing.T) {
	s, o, _ := hostedServiceFixture(t)
	ctx := context.Background()
	o, err := s.entClient.PaymentOrder.UpdateOneID(o.ID).SetOrderType("subscription").SetPlanID(100).SetSubscriptionGroupID(7).SetSubscriptionDays(30).Save(ctx)
	require.NoError(t, err)
	repo := newSubscriptionUserSubRepoStub()
	s.groupRepo = &subscriptionGroupRepoStub{group: &Group{ID: 7, Status: "active", SubscriptionType: SubscriptionTypeSubscription}}
	s.subscriptionSvc = NewSubscriptionService(s.groupRepo, repo, nil, nil, nil)
	require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "processing"), "stripe_hosted"))
	require.Zero(t, repo.createCalls)
	require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "success"), "stripe_hosted"))
	first, err := repo.GetByUserIDAndGroupID(ctx, o.UserID, 7)
	require.NoError(t, err)
	expires := first.ExpiresAt
	require.NoError(t, s.HandlePaymentNotification(ctx, hostedNotification(o, "success"), "stripe_hosted"))
	second, err := repo.GetByUserIDAndGroupID(ctx, o.UserID, 7)
	require.NoError(t, err)
	require.Equal(t, expires, second.ExpiresAt)
	require.Equal(t, 1, repo.createCalls)
}
