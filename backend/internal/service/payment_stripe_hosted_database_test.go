//go:build unit

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/enttest"
	"github.com/liulixin-lex/xy2api/ent/paymentorder"
	"github.com/liulixin-lex/xy2api/internal/payment"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
)

// Set STRIPE_HOSTED_TEST_DSN to a disposable PostgreSQL database. Each test gets
// its own schema and exercises the same code as the fast SQLite suite.
func newHostedDatabase(t *testing.T) *dbent.Client {
	t.Helper()
	dsn := os.Getenv("STRIPE_HOSTED_TEST_DSN")
	if dsn == "" {
		return newPaymentConfigServiceTestClient(t)
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := "hosted_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = admin.Close() })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(12)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestStripeHostedCancelPaymentRace(t *testing.T) {
	for _, outcome := range []string{"paid", "processing", "expired", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			svc, order, credited := hostedServiceFixture(t)
			ctx := context.Background()
			key := []byte("0123456789abcdef0123456789abcdef")
			cfg := NewPaymentConfigService(svc.entClient, &paymentConfigSettingRepoStub{values: map[string]string{}}, key)
			inst, err := cfg.CreateProviderInstance(ctx, CreateProviderInstanceRequest{Name: "Hosted", ProviderKey: "stripe_hosted", Enabled: true, Config: map[string]string{"secretKey": "sk_test_fixture", "webhookSecret": "whsec_fixture", "currency": "CNY"}})
			require.NoError(t, err)
			require.EqualValues(t, 1, inst.ID)
			svc.configService = cfg
			svc.loadBalancer = payment.NewDefaultLoadBalancer(svc.entClient, key)
			var expireCalls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/account" {
					_, _ = w.Write([]byte(`{"id":"acct_test"}`))
					return
				}
				if r.Method == http.MethodPost {
					expireCalls.Add(1)
					if outcome != "expired" {
						w.WriteHeader(409)
						_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"session state changed"}}`))
						return
					}
				}
				cs := &stripe.CheckoutSession{ID: "cs_test_1", Mode: "payment", Status: "open", PaymentStatus: "unpaid", Currency: "cny", AmountTotal: 8000, ClientReferenceID: order.OutTradeNo, Metadata: map[string]string{"orderId": order.OutTradeNo, "providerInstanceId": "1", "providerKey": "stripe_hosted", "accountId": "acct_test"}}
				if outcome == "processing" {
					cs.Status = "complete"
				} else if expireCalls.Load() > 0 && outcome == "paid" {
					cs.Status, cs.PaymentStatus = "complete", "paid"
				} else if expireCalls.Load() > 0 && outcome == "expired" {
					cs.Status = "expired"
				}
				_ = json.NewEncoder(w).Encode(cs)
			}))
			t.Cleanup(server.Close)
			previous := stripe.GetBackend(stripe.APIBackend)
			stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), MaxNetworkRetries: stripe.Int64(0)}))
			t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, previous) })
			result, err := svc.cancelHostedOrder(ctx, order, OrderStatusCancelled, "user")
			current, readErr := svc.entClient.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, readErr)
			switch outcome {
			case "paid":
				require.NoError(t, err)
				require.Equal(t, checkPaidResultAlreadyPaid, result)
				require.Equal(t, OrderStatusCompleted, current.Status)
				require.Equal(t, 80.0, *credited)
			case "processing":
				require.Error(t, err)
				require.Equal(t, OrderStatusProcessing, current.Status)
				require.Zero(t, expireCalls.Load())
			case "expired":
				require.NoError(t, err)
				require.Equal(t, checkPaidResultCancelled, result)
				require.Equal(t, OrderStatusCancelled, current.Status)
			case "unavailable":
				require.Error(t, err)
				require.Equal(t, OrderStatusPending, current.Status)
			}
		})
	}
}

func TestStripeHostedDatabaseCreateRecoveryAndConcurrentLimits(t *testing.T) {
	if os.Getenv("STRIPE_HOSTED_TEST_DSN") == "" {
		t.Skip("requires disposable PostgreSQL for row locks")
	}
	ctx := context.Background()
	client := newHostedDatabase(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	settings := &paymentConfigSettingRepoStub{values: map[string]string{
		SettingPaymentEnabled: "true", SettingEnabledPaymentTypes: "stripe_hosted",
		SettingKeyFrontendURL: "https://app.example", SettingMaxPendingOrders: "1",
		SettingBalanceRechargeMult: "2", SettingRechargeFeeRate: "5", SettingOrderTimeoutMinutes: "5",
	}}
	cfg := NewPaymentConfigService(client, settings, key)
	inst, err := cfg.CreateProviderInstance(ctx, CreateProviderInstanceRequest{Name: "Hosted", ProviderKey: "stripe_hosted", Enabled: true, Config: map[string]string{"secretKey": "sk_test_fixture", "webhookSecret": "whsec_fixture", "currency": "CNY"}})
	require.NoError(t, err)
	u, err := client.User.Create().SetEmail("create@example.test").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	users := &mockUserRepo{getByIDUser: &User{ID: u.ID, Email: u.Email, Status: "active"}}
	svc := NewPaymentService(client, payment.NewRegistry(), payment.NewDefaultLoadBalancer(client, key), nil, nil, cfg, users, nil, nil)
	var mu sync.Mutex
	var frozen url.Values
	var session *stripe.CheckoutSession
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/account" {
			_, _ = w.Write([]byte(`{"id":"acct_fixture"}`))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			require.NoError(t, r.ParseForm())
			if frozen == nil {
				frozen = r.PostForm
				amount, _ := strconv.ParseInt(frozen.Get("line_items[0][price_data][unit_amount]"), 10, 64)
				expiry, _ := strconv.ParseInt(frozen.Get("expires_at"), 10, 64)
				session = &stripe.CheckoutSession{ID: "cs_test_recovery", URL: "https://checkout.stripe.com/c/pay/cs_test_recovery", ExpiresAt: expiry, Mode: "payment", Status: "open", PaymentStatus: "unpaid", Currency: "cny", AmountTotal: amount, ClientReferenceID: frozen.Get("client_reference_id"), Metadata: map[string]string{"orderId": frozen.Get("metadata[orderId]"), "providerInstanceId": strconv.FormatInt(inst.ID, 10), "providerKey": "stripe_hosted", "accountId": "acct_fixture"}}
			} else {
				require.Equal(t, frozen, r.PostForm)
			}
			require.Equal(t, "checkout-"+strconv.FormatInt(inst.ID, 10)+"-"+session.ClientReferenceID, r.Header.Get("Idempotency-Key"))
			if attempts.Add(1) == 1 {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"response lost"}}`))
				return
			}
		}
		_ = json.NewEncoder(w).Encode(session)
	}))
	t.Cleanup(server.Close)
	previous := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), MaxNetworkRetries: stripe.Int64(0)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, previous) })
	req := CreateOrderRequest{UserID: u.ID, PaymentType: "stripe_hosted", Amount: 80, OrderType: "balance", IdempotencyKey: "hosted_database_retry_1", SrcHost: "attacker.example", ReturnURL: "https://attacker.example"}
	_, err = svc.CreateOrder(ctx, req)
	require.Error(t, err)
	orders, err := client.PaymentOrder.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "PENDING", orders[0].Status)
	results := make(chan *CreateOrderResponse, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, err := svc.CreateOrder(ctx, req); results <- r; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for r := range results {
		require.Equal(t, orders[0].ID, r.OrderID)
		require.Equal(t, "redirect", r.PaymentMode)
		require.Equal(t, 160.0, r.Amount)
		require.Equal(t, 84.0, r.PayAmount)
		require.Empty(t, r.ClientSecret)
		require.Contains(t, r.PayURL, "checkout.stripe.com")
	}
	require.Equal(t, "8400", frozen.Get("line_items[0][price_data][unit_amount]"))
	require.NotContains(t, frozen.Get("success_url"), "attacker")
	require.GreaterOrEqual(t, orders[0].ExpiresAt.Sub(orders[0].CreatedAt), 30*time.Minute)
	count, err := client.PaymentOrder.Query().Where(paymentorder.UserIDEQ(u.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	req.Amount = 81
	_, err = svc.CreateOrder(ctx, req)
	require.Error(t, err)
	req.IdempotencyKey = "hosted_database_second"
	_, err = svc.CreateOrder(ctx, req)
	require.Error(t, err)
	// An early callback can bind the ID before the create response stores its URL.
	_, err = client.PaymentOrder.UpdateOneID(orders[0].ID).ClearPayURL().Save(ctx)
	require.NoError(t, err)
	r, err := svc.ResumeStripeHostedOrder(ctx, orders[0].ID, u.ID)
	require.NoError(t, err)
	require.NotEmpty(t, r.PayURL)
	fmt.Printf("HOSTED_DATABASE order_count=%d retry_session=%s credited_quote=160 charged_minor=8400 max_pending_enforced=true\n", count, session.ID)
}

func TestStripeHostedDatabaseSerializesNewRequests(t *testing.T) {
	if os.Getenv("STRIPE_HOSTED_TEST_DSN") == "" {
		t.Skip("requires PostgreSQL row locks")
	}
	for _, sameKey := range []bool{true, false} {
		t.Run(strconv.FormatBool(sameKey), func(t *testing.T) {
			ctx := context.Background()
			client := newHostedDatabase(t)
			key := []byte("0123456789abcdef0123456789abcdef")
			cfg := NewPaymentConfigService(client, nil, key)
			config := map[string]string{"secretKey": "sk_test_fixture", "webhookSecret": "whsec_fixture", "currency": "CNY"}
			inst, err := cfg.CreateProviderInstance(ctx, CreateProviderInstanceRequest{Name: "Hosted", ProviderKey: "stripe_hosted", Enabled: true, Config: config})
			require.NoError(t, err)
			user, err := client.User.Create().SetEmail("lock@example.test").SetPasswordHash("hash").Save(ctx)
			require.NoError(t, err)
			config["paymentMode"] = "redirect"
			sel := &payment.InstanceSelection{InstanceID: strconv.FormatInt(inst.ID, 10), ProviderKey: "stripe_hosted", Config: config, PaymentMode: "redirect"}
			svc := &PaymentService{entClient: client, configService: cfg}
			var wg sync.WaitGroup
			var successes atomic.Int64
			for i := 0; i < 6; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					id := "new_request_same_key"
					if !sameKey {
						id += strconv.Itoa(i)
					}
					req := CreateOrderRequest{UserID: user.ID, Amount: 80, OrderType: "balance", PaymentType: "stripe_hosted", IdempotencyKey: id}
					req.HostedSnapshot = map[string]any{"request_fingerprint": hostedRequestFingerprint(req)}
					_, err := svc.createOrderInTx(ctx, req, &User{ID: user.ID, Email: user.Email}, nil, &PaymentConfig{MaxPendingOrders: 1, DailyLimit: 100}, 80, 80, 0, 80, sel)
					if err == nil {
						successes.Add(1)
					}
				}(i)
			}
			wg.Wait()
			count, err := client.PaymentOrder.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			if sameKey {
				require.Equal(t, int64(6), successes.Load())
			} else {
				require.Equal(t, int64(1), successes.Load())
			}
		})
	}
}
