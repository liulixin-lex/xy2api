//go:build integration

package repository

import (
	"context"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRecoveryObservabilityMigrationAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	migration, err := migrations.FS.ReadFile("283_recovery_observability.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = integrationDB.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "recovery"})
	account := mustCreateAccount(t, client, &service.Account{Name: "recovery-" + uuid.NewString()})
	elapsed := int64(28000)
	for _, mode := range []string{"single", "batch", "best_effort", "no_result"} {
		for _, value := range []*int64{nil, &elapsed} {
			log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, Model: "gpt-5", RequestID: uuid.NewString(), CreatedAt: time.Now(), EndToEndFirstOutputMs: value}
			prepared := prepareUsageLogInsert(log)
			switch mode {
			case "single":
				_, err = repo.createSingle(ctx, integrationDB, log)
			case "batch":
				batchKey := usageLogBatchKey(log.RequestID, log.APIKeyID)
				_, _, _, err = repo.batchInsertUsageLogs(integrationDB, []string{batchKey}, map[string]usageLogInsertPrepared{batchKey: prepared})
			case "best_effort":
				q, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
				_, err = integrationDB.ExecContext(ctx, q, args...)
			case "no_result":
				err = execUsageLogInsertNoResult(ctx, integrationDB, prepared)
			}
			require.NoError(t, err, mode)
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", log.RequestID, log.APIKeyID).Scan(&log.ID))
			got, e := repo.GetByID(ctx, log.ID)
			require.NoError(t, e)
			require.Equal(t, value, got.EndToEndFirstOutputMs, mode)
		}
	}
	ops := NewOpsRepository(integrationDB)
	completed := "completed"
	for _, batch := range []bool{false, true} {
		for _, value := range []*int64{nil, &elapsed} {
			entry := &service.OpsInsertErrorLogInput{RequestID: uuid.NewString(), ErrorPhase: "upstream", ErrorType: "test", Severity: "error", CreatedAt: time.Now(), EndToEndFirstOutputMs: value}
			if value != nil {
				entry.RecoveryOutcome = &completed
			}
			var id int64
			if batch {
				_, err = ops.BatchInsertErrorLogs(ctx, []*service.OpsInsertErrorLogInput{entry})
				require.NoError(t, err)
				err = integrationDB.QueryRowContext(ctx, "SELECT id FROM ops_error_logs WHERE request_id=$1", entry.RequestID).Scan(&id)
			} else {
				id, err = ops.InsertErrorLog(ctx, entry)
			}
			require.NoError(t, err)
			got, e := ops.GetErrorLogByID(ctx, id)
			require.NoError(t, e)
			require.Equal(t, value, got.EndToEndFirstOutputMs)
			require.Equal(t, entry.RecoveryOutcome, got.RecoveryOutcome)
		}
	}
}
