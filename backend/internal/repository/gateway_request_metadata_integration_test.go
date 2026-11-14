//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayRequestMetadataSurvivesBillingIntentAndRead(t *testing.T) {
	r, cmd := newBillingIntentFixture(t)
	id, result, switches, fallback, takeover := uuid.NewString(), int16(2), int16(1), true, false
	cmd.Usage.GatewayRequestID, cmd.Usage.Result, cmd.Usage.Switches = &id, &result, &switches
	cmd.Usage.Fallback, cmd.Usage.Takeover = &fallback, &takeover
	cmd.Usage.EndToEndDurationMs = new(int64(300))
	_, err := r.Apply(context.Background(), cmd)
	require.NoError(t, err)
	var usageID int64
	require.NoError(t, integrationDB.QueryRow("SELECT id FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&usageID))
	repo := newUsageLogRepositoryWithSQL(testEntClient(t), integrationDB)
	row, err := repo.GetByID(context.Background(), usageID)
	require.NoError(t, err)
	require.Equal(t, cmd.RequestID, row.RequestID)
	require.Equal(t, &id, row.GatewayRequestID)
	require.Equal(t, &result, row.Result)
	require.Equal(t, &switches, row.Switches)
	require.Equal(t, &fallback, row.Fallback)
	require.Equal(t, &takeover, row.Takeover)
	require.Equal(t, new(int64(300)), row.EndToEndDurationMs)
	// Recovery/idempotence keeps the original frozen request facts.
	result = 3
	cmd.Usage.EndToEndDurationMs = new(int64(999))
	_, err = r.Apply(context.Background(), cmd)
	require.NoError(t, err)
	row, err = repo.GetByID(context.Background(), usageID)
	require.NoError(t, err)
	require.EqualValues(t, 2, *row.Result)
	require.Equal(t, new(int64(300)), row.EndToEndDurationMs)
}

func TestGatewayRequestCleanupSharesRetentionAndBillingFilter(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newUsageLogRepositoryWithSQL(tx.Client(), tx)
	at := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	typ := int16(service.RequestTypeStream)
	require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{RequestID: id, CreatedAt: at, Result: 3, RequestType: &typ}))
	cleanup := newUsageCleanupRepositoryWithSQL(tx.Client(), tx)
	billing := int8(0)
	filters := service.UsageCleanupFilters{StartTime: at.Add(-time.Second), EndTime: at.Add(time.Second), RequestType: &typ, BillingType: &billing}
	_, err := cleanup.DeleteUsageLogsBatch(ctx, filters, 100)
	require.NoError(t, err)
	var n int
	require.NoError(t, scanSingleRow(ctx, tx, "SELECT count(*) FROM gateway_unbilled_requests WHERE request_id=$1", []any{id}, &n))
	require.Equal(t, 1, n)
	filters.BillingType = nil
	_, err = cleanup.DeleteUsageLogsBatch(ctx, filters, 100)
	require.NoError(t, err)
	require.NoError(t, scanSingleRow(ctx, tx, "SELECT count(*) FROM gateway_unbilled_requests WHERE request_id=$1", []any{id}, &n))
	require.Zero(t, n)
}

func TestGatewayRequestAutomaticRetention(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newUsageLogRepositoryWithSQL(tx.Client(), tx)
	cutoff := time.Now().UTC().Truncate(time.Second).Add(-30 * 24 * time.Hour)
	expired, retained := uuid.NewString(), uuid.NewString()
	for id, at := range map[string]time.Time{expired: cutoff.Add(-time.Second), retained: cutoff} {
		require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{RequestID: id, CreatedAt: at, Result: 3}))
	}
	cleanup := newDashboardAggregationRepositoryWithSQL(tx)
	require.NoError(t, cleanup.CleanupUsageLogs(ctx, cutoff))
	var n int
	require.NoError(t, scanSingleRow(ctx, tx, "SELECT count(*) FROM gateway_unbilled_requests WHERE request_id=$1", []any{expired}, &n))
	require.Zero(t, n)
	require.NoError(t, scanSingleRow(ctx, tx, "SELECT count(*) FROM gateway_unbilled_requests WHERE request_id=$1", []any{retained}, &n))
	require.Equal(t, 1, n)
}
