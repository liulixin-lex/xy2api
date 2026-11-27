//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMeteringAdminStatsAndPersistence(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	u := mustCreateUser(t, client, &service.User{Email: "metering@example.test"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: uuid.NewString(), Name: "metering"})
	a := mustCreateAccount(t, client, &service.Account{Name: "metering"})
	g := mustCreateGroup(t, client, &service.Group{Name: "metering"})
	at := time.Now().UTC()
	start, end := at.Add(-time.Minute), at.Add(time.Minute)
	for _, estimate := range []*float64{nil, new(0.0), new(0.9)} {
		row := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, GroupID: &g.ID,
			RequestID: uuid.NewString(), Model: "fixture", CreatedAt: at, TotalCost: 2, ActualCost: 3,
			AccountStatsCost: new(1.0), AccountRateMultiplier: new(0.5), SupplierCostEstimate: estimate,
			MeteringAdjustment: json.RawMessage(`{"v":1,"supplier_cost_source":"unknown"}`)}
		_, err := repo.Create(ctx, row)
		require.NoError(t, err)
		got, err := repo.GetByID(ctx, row.ID)
		require.NoError(t, err)
		require.Equal(t, estimate, got.SupplierCostEstimate)
		require.JSONEq(t, string(row.MeteringAdjustment), string(got.MeteringAdjustment))
	}
	require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{RequestID: uuid.NewString(), CreatedAt: at, UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID, Model: "fixture", Result: 3}))
	stats, err := repo.AdminRequestStats(ctx, UsageLogFilters{GroupID: g.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.EqualValues(t, 4, stats.TotalRequests)
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, 1.4, *stats.TotalAccountCost, 1e-10)
}

func TestMeteringLegacyIntentReplayDebitsOnce(t *testing.T) {
	r, cmd := newBillingIntentFixture(t)
	ctx := context.Background()
	cmd.Normalize()
	// Persist an old-format intent before the new writer retries the same bill.
	old, err := json.Marshal(cmd)
	require.NoError(t, err)
	_, err = r.db.ExecContext(ctx, `INSERT INTO usage_billing_intents(request_id,api_key_id,user_id,request_fingerprint,command) VALUES($1,$2,$3,$4,$5::jsonb)`, cmd.RequestID, cmd.APIKeyID, cmd.UserID, cmd.RequestFingerprint, string(old))
	require.NoError(t, err)
	cmd.Usage.SupplierCostEstimate = new(0.8)
	cmd.Usage.MeteringAdjustment = json.RawMessage(`{"v":1,"supplier_cost_source":"contract"}`)
	for i := 0; i < 2; i++ {
		result, err := r.Apply(ctx, cmd)
		require.NoError(t, err)
		require.Equal(t, i == 0, result.Applied)
	}
	assertBillingIntentMoney(t, cmd, 98.75, 1)
	var missing bool
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT supplier_cost_estimate IS NULL AND metering_adjustment IS NULL FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID).Scan(&missing))
	require.True(t, missing, "the first durable intent remains authoritative")
}
