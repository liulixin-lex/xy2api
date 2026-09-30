//go:build integration

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeReviewBillingUserRepo struct {
	service.UserRepository
	db *sql.DB
}

func (r *nativeReviewBillingUserRepo) GetByID(ctx context.Context, id int64) (*service.User, error) {
	var balance float64
	if err := r.db.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", id).Scan(&balance); err != nil {
		return nil, err
	}
	return &service.User{ID: id, Status: service.StatusActive, Balance: balance, Concurrency: 1}, nil
}

// This fixture uses the production UsageBillingRepository transaction against
// PostgreSQL. Only the minimum touched schema is isolated; no mock substitutes
// for balance mutation or the repository's unique-key/fingerprint transaction.
func TestReviewNativeBackgroundRealBillingSettlesOnce(t *testing.T) {
	reviewNativeBackgroundRealBilling(t, "completed")
}

func TestReviewNativeBackgroundRealBillingTerminalAndCancellation(t *testing.T) {
	for _, status := range []string{"failed", "incomplete", "cancelled", "permission_revoked"} {
		t.Run(status, func(t *testing.T) { reviewNativeBackgroundRealBilling(t, status) })
	}
}

func reviewNativeBackgroundRealBilling(t *testing.T, terminalStatus string) {
	t.Helper()
	dsn := os.Getenv("NATIVE_REVIEW_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("SCHEDULING_TEST_POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN is required by the native integration gate")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("native_background_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	address, err := url.Parse(dsn)
	require.NoError(t, err)
	query := address.Query()
	query.Set("search_path", schema)
	address.RawQuery = query.Encode()
	db, err := sql.Open("postgres", address.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE users(id bigint PRIMARY KEY,balance numeric(30,12) NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),deleted_at timestamptz);
CREATE TABLE usage_billing_dedup(id bigserial PRIMARY KEY,request_id text NOT NULL,api_key_id bigint NOT NULL,request_fingerprint text NOT NULL,UNIQUE(request_id,api_key_id));
CREATE TABLE usage_billing_dedup_archive(request_id text NOT NULL,api_key_id bigint NOT NULL,request_fingerprint text NOT NULL,UNIQUE(request_id,api_key_id));
INSERT INTO users(id,balance) VALUES(71,100);`)
	require.NoError(t, err)
	billingRepo := repository.NewUsageBillingRepository(nil, db)
	f := newNativeRecoveryFixtureOptions(t, "background", nativeRecoveryOptions{standard: true, billingRepo: billingRepo, userRepo: &nativeReviewBillingUserRepo{db: db}, terminalStatus: terminalStatus})
	f.server.Client().Timeout = 5 * time.Second
	body := `{"model":"gpt-5.6-sol","input":"real transaction fixture","background":true,"stream":false}`
	makeCreate := func() *http.Response {
		req, e := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "real-billing-idempotency")
		response, e := f.server.Client().Do(req)
		require.NoError(t, e)
		return response
	}
	initial := makeCreate()
	raw, err := io.ReadAll(initial.Body)
	require.NoError(t, err)
	_ = initial.Body.Close()
	require.Equal(t, 200, initial.StatusCode, string(raw))
	require.Equal(t, "queued", gjson.GetBytes(raw, "status").String())
	var balance float64
	var claims int
	require.NoError(t, db.QueryRow("SELECT balance FROM users WHERE id=71").Scan(&balance))
	require.Equal(t, 100.0, balance)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_billing_dedup").Scan(&claims))
	require.Zero(t, claims, "queued create must not claim the final settlement key")
	activeDuplicate := makeCreate()
	_, _ = io.Copy(io.Discard, activeDuplicate.Body)
	_ = activeDuplicate.Body.Close()
	require.Equal(t, http.StatusConflict, activeDuplicate.StatusCode)
	wantedState := nativeResponseTerminalState(terminalStatus)
	switch terminalStatus {
	case "permission_revoked":
		f.accessRevoked.Store(true)
		wantedState = responseturn.StateCancelled
	case "cancelled":
		cancelled, e := f.server.Client().Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
		require.NoError(t, e)
		_, _ = io.Copy(io.Discard, cancelled.Body)
		_ = cancelled.Body.Close()
		require.Equal(t, 200, cancelled.StatusCode)
	default:
		f.finishNext()
	}
	require.Eventually(t, func() bool { return f.turn(t).Snapshot().State == wantedState }, 4*time.Second, time.Millisecond)
	var usage *service.UsageLog
	select {
	case usage = <-f.usage:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal billing record missing")
	}
	require.Equal(t, 4, usage.InputTokens)
	require.Equal(t, 1, usage.OutputTokens)
	require.Greater(t, usage.ActualCost, 0.0)
	require.NoError(t, db.QueryRow("SELECT balance FROM users WHERE id=71").Scan(&balance))
	require.InDelta(t, 100-usage.ActualCost, balance, 1e-10)
	settledBalance := balance
	wantedHTTPStatus := http.StatusOK
	if terminalStatus == "permission_revoked" {
		wantedHTTPStatus = http.StatusNotFound
	}
	for i := 0; i < 4; i++ {
		duplicate := makeCreate()
		duplicateBody, e := io.ReadAll(duplicate.Body)
		require.NoError(t, e)
		_ = duplicate.Body.Close()
		require.Equal(t, wantedHTTPStatus, duplicate.StatusCode)
		if wantedHTTPStatus == http.StatusOK {
			require.Equal(t, terminalStatus, gjson.GetBytes(duplicateBody, "status").String())
		}
		read, e := f.server.Client().Get(f.server.URL + "/v1/responses/resp_fixture")
		require.NoError(t, e)
		_, _ = io.Copy(io.Discard, read.Body)
		_ = read.Body.Close()
		require.Equal(t, wantedHTTPStatus, read.StatusCode)
		cancelled, e := f.server.Client().Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
		require.NoError(t, e)
		_, _ = io.Copy(io.Discard, cancelled.Body)
		_ = cancelled.Body.Close()
		require.Equal(t, wantedHTTPStatus, cancelled.StatusCode)
	}
	require.NoError(t, db.QueryRow("SELECT balance FROM users WHERE id=71").Scan(&balance))
	require.Equal(t, settledBalance, balance)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_billing_dedup").Scan(&claims))
	require.Equal(t, 1, claims)
	require.Equal(t, int64(1), f.createCount.Load())
	if terminalStatus == "cancelled" || terminalStatus == "permission_revoked" {
		require.Equal(t, int64(1), f.cancelCount.Load())
	} else {
		require.Zero(t, f.cancelCount.Load())
	}
	var billingID string
	require.NoError(t, db.QueryRow("SELECT request_id FROM usage_billing_dedup").Scan(&billingID))
	require.Equal(t, "fixture-background-usage-id", billingID, "retrieve request IDs must never replace original settlement identity")
	select {
	case <-f.usage:
		t.Fatal("a read, cancel or duplicate create settled again")
	default:
	}
	t.Logf("real PostgreSQL status=%s balance %.12f -> %.12f; terminal usage input=4 output=1; dedup claims=%d; original billing ID=%s; upstream creates=%d", terminalStatus, 100.0, balance, claims, billingID, f.createCount.Load())
}
