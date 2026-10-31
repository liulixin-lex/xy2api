//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCacheTTLMigrationAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	migration, err := migrations.FS.ReadFile("286_anthropic_cache_ttl.sql")
	require.NoError(t, err)
	// Exercise pre-migration rows on the same PostgreSQL, without altering shared tables.
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE api_keys (id bigint PRIMARY KEY, quota_used numeric NOT NULL); CREATE TEMP TABLE usage_logs (id bigint PRIMARY KEY, input_tokens integer); INSERT INTO api_keys VALUES (1,17); INSERT INTO usage_logs VALUES (1,23)`)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var mode string
	var used int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT anthropic_cache_ttl_mode, quota_used FROM api_keys WHERE id=1`).Scan(&mode, &used))
	require.Equal(t, "off", mode)
	require.Equal(t, 17, used)
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys (id,quota_used) VALUES (2,0)`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT anthropic_cache_ttl_mode FROM api_keys WHERE id=2`).Scan(&mode))
	require.Equal(t, "inherit", mode)
	var nullable int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE input_tokens=23 AND anthropic_cache_ttl_policy IS NULL AND anthropic_cache_ttl_applied IS NULL`).Scan(&nullable))
	require.Equal(t, 1, nullable)
	_, err = tx.ExecContext(ctx, `UPDATE api_keys SET anthropic_cache_ttl_mode='invalid' WHERE id=1`)
	require.Error(t, err)
	require.NoError(t, tx.Rollback())

	tag := "ttl-" + uuid.NewString()
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM usage_logs WHERE request_id LIKE $1 || '%'`,
			`DELETE FROM api_keys WHERE name = $1`,
			`DELETE FROM users WHERE email = $1 || '@example.com'`,
			`DELETE FROM accounts WHERE name = $1`,
		} {
			_, e := integrationDB.ExecContext(context.Background(), query, tag)
			require.NoError(t, e)
		}
	})
	client := testEntClient(t)
	keyRepo := NewAPIKeyRepository(client, integrationDB)
	usageRepo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: tag + "@example.com"})
	key := &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: tag, Status: service.StatusActive}
	require.NoError(t, keyRepo.Create(ctx, key))
	require.Equal(t, "inherit", key.AnthropicCacheTTLMode)
	for _, value := range []string{"off", "5m", "1h", "inherit"} {
		key.AnthropicCacheTTLMode = value
		require.NoError(t, keyRepo.Update(ctx, key, service.APIKeyUpdateFields{AnthropicCacheTTLMode: true}))
		for _, read := range []func(context.Context, string) (*service.APIKey, error){keyRepo.GetByKey, keyRepo.GetByKeyForAuth} {
			got, e := read(ctx, key.Key)
			require.NoError(t, e)
			require.Equal(t, value, got.AnthropicCacheTTLMode)
		}
	}
	stale, err := keyRepo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	_, err = keyRepo.IncrementQuotaUsed(ctx, key.ID, 19)
	require.NoError(t, err)
	stale.AnthropicCacheTTLMode = "1h"
	require.NoError(t, keyRepo.Update(ctx, stale, service.APIKeyUpdateFields{AnthropicCacheTTLMode: true}))
	got, err := keyRepo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, 19.0, got.QuotaUsed)
	// A stale IP-only edit must not overwrite a concurrently changed TTL.
	stale.AnthropicCacheTTLMode = "off"
	stale.IPWhitelist = []string{"127.0.0.1"}
	require.NoError(t, keyRepo.Update(ctx, stale, service.APIKeyUpdateFields{IPRules: true}))
	got, err = keyRepo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, "1h", got.AnthropicCacheTTLMode)
	require.Equal(t, 19.0, got.QuotaUsed)
	account := mustCreateAccount(t, client, &service.Account{Name: tag})
	policy := "1h"
	yes, no := true, false
	for _, path := range []string{"single", "batch", "best_effort", "no_result"} {
		for _, applied := range []*bool{nil, &yes, &no} {
			log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, Model: "claude-sonnet-5-5", RequestID: tag + "-" + uuid.NewString()[:12], CreatedAt: time.Now(), AnthropicCacheTTLApplied: applied}
			if applied != nil {
				log.AnthropicCacheTTLPolicy = &policy
			}
			prepared := prepareUsageLogInsert(log)
			switch path {
			case "single":
				_, err = usageRepo.createSingle(ctx, integrationDB, log)
			case "batch":
				batchKey := usageLogBatchKey(log.RequestID, log.APIKeyID)
				_, _, _, err = usageRepo.batchInsertUsageLogs(integrationDB, []string{batchKey}, map[string]usageLogInsertPrepared{batchKey: prepared})
			case "best_effort":
				q, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
				_, err = integrationDB.ExecContext(ctx, q, args...)
			case "no_result":
				err = execUsageLogInsertNoResult(ctx, integrationDB, prepared)
			}
			require.NoError(t, err, path)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, log.RequestID, log.APIKeyID).Scan(&log.ID))
			stored, e := usageRepo.GetByID(ctx, log.ID)
			require.NoError(t, e)
			require.Equal(t, log.AnthropicCacheTTLPolicy, stored.AnthropicCacheTTLPolicy, path)
			require.Equal(t, log.AnthropicCacheTTLApplied, stored.AnthropicCacheTTLApplied, path)
		}
	}
}

func TestAnthropicCacheTTLAuthRedisRoundTrip(t *testing.T) {
	ctx := context.Background()
	cache := NewAPIKeyCache(testRedis(t))
	cacheKey := "ttl-auth-" + uuid.NewString()
	t.Cleanup(func() { require.NoError(t, cache.DeleteAuthCache(context.Background(), cacheKey)) })
	for _, mode := range []string{"inherit", "off", "5m", "1h"} {
		entry := &service.APIKeyAuthCacheEntry{Snapshot: &service.APIKeyAuthSnapshot{Version: 27, APIKeyID: 1, AnthropicCacheTTLMode: mode}}
		require.NoError(t, cache.SetAuthCache(ctx, cacheKey, entry, time.Minute))
		got, err := cache.GetAuthCache(ctx, cacheKey)
		require.NoError(t, err)
		require.Equal(t, entry, got)
	}
}
