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

func TestUpstreamV0215MigrationUpgrade(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("v0215_upgrade"), tcpostgres.WithUsername("postgres"),
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
		if name >= "270_" {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		require.NoError(t, err)
		previous[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, previous, 316, "published 0.2.6 migration set")
	require.NoError(t, applyMigrationsFS(ctx, db, previous))
	var userID, groupID int64
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,balance) VALUES('v0215@example.test','fixture',123.45) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRow(`INSERT INTO groups(name,platform) VALUES('upgrade-v0215','composite') RETURNING id`).Scan(&groupID))
	_, err = db.Exec(`INSERT INTO composite_model_routes(group_id,public_model,target_platform) VALUES($1,'retained','typesafe')`, groupID)
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ApplyMigrations(ctx, db), "upgrade must be idempotent")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count))
	require.Equal(t, len(names), count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pg_constraint WHERE conname IN ('user_platform_quotas_platform_check','composite_model_routes_target_platform_check')`).Scan(&count))
	require.Zero(t, count, "platform catalog owns validation after appended migration 271")
	_, err = db.Exec(`INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES($1,'command_code',10)`, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO composite_model_routes(group_id,public_model,target_platform) VALUES($1,'new','cline')`, groupID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM composite_model_routes WHERE group_id=$1`, groupID).Scan(&count))
	require.Equal(t, 2, count)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
	require.Equal(t, 123.45, balance)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM share_scheduling_policies`).Scan(&count))
	require.Zero(t, count, "upgrade without legacy policy does not enable share routing")
}
