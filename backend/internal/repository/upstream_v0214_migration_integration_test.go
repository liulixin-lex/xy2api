//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The suite harness covers a fresh database. This test upgrades the published
// XY2API 0.2.2 schema with existing money and routing data through the real runner.
func TestUpstreamV0214MigrationUpgrade(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("upstream_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("fixture"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	previous := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name == "268_add_payment_order_bonus_amount.sql" || name == "269_add_typesafe_platform.sql" {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		require.NoError(t, err)
		previous[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, previous, 314)
	require.NoError(t, applyMigrationsFS(ctx, db, previous))
	var userID, groupID, orderID int64
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,balance) VALUES('upgrade@example.test','fixture',123.45) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRow(`INSERT INTO groups(name,platform) VALUES('upgrade','composite') RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRow(`INSERT INTO payment_orders(user_id,amount,pay_amount,expires_at,out_trade_no) VALUES($1,80,40,NOW()+INTERVAL '1 hour','upgrade-legacy') RETURNING id`, userID).Scan(&orderID))
	_, err = db.Exec(`INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES($1,'opencode_go',10)`, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO composite_model_routes(group_id,public_model,target_platform) VALUES($1,'legacy','opencode_go')`, groupID)
	require.NoError(t, err)

	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ApplyMigrations(ctx, db), "upgrade must be idempotent")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count))
	require.Equal(t, len(names), count)
	var amount, payAmount, bonus, balance float64
	require.NoError(t, db.QueryRow(`SELECT amount,pay_amount,bonus_amount FROM payment_orders WHERE id=$1`, orderID).Scan(&amount, &payAmount, &bonus))
	require.Equal(t, 80.0, amount)
	require.Equal(t, 40.0, payAmount)
	require.Zero(t, bonus, "existing payments must not acquire a promotion")
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
	require.Equal(t, 123.45, balance)
	_, err = db.Exec(`INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES($1,'typesafe',10)`, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO composite_model_routes(group_id,public_model,target_platform) VALUES($1,'jev-latest','typesafe')`, groupID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES($1,'invalid-platform',10)`, userID)
	require.Error(t, err, "platform constraint must remain enforced")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM composite_model_routes WHERE group_id=$1`, groupID).Scan(&count))
	require.Equal(t, 2, count, "old and new platform routes coexist")
}
