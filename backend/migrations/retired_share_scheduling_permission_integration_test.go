//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestRetiredShareSchedulingPermissionMigrationPreservesOperatorAccess(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("operator_permission_upgrade"),
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("fixture"),
		tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(ctx, "CREATE TABLE users (id BIGINT PRIMARY KEY); INSERT INTO users(id) VALUES (1), (2)")
	require.NoError(t, err)
	schema, err := FS.ReadFile("282_admin_operator_permissions.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(schema))
	require.NoError(t, err)

	legacy := `{"modules":{"share_scheduling":"manage","accounts":"view","users":"manage"},"flags":["data.export","users.balance_adjust"]}`
	untouched := `{"modules":{"accounts":"manage"},"flags":["accounts.upstream_address"]}`
	_, err = db.ExecContext(ctx, `INSERT INTO admin_operator_permissions(user_id,permissions) VALUES (1,$1::jsonb),(2,$2::jsonb)`, legacy, untouched)
	require.NoError(t, err)
	_, err = operatoraccess.DecodeWire([]byte(legacy))
	require.ErrorIs(t, err, operatoraccess.ErrInvalid, "the retired module invalidates the stored permissions before migration")

	migration, err := FS.ReadFile("295_remove_retired_share_scheduling_permission.sql")
	require.NoError(t, err)
	result, err := db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	updated, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), updated, "only the row carrying the retired module is updated")

	var migrated, unchanged []byte
	require.NoError(t, db.QueryRowContext(ctx, "SELECT permissions FROM admin_operator_permissions WHERE user_id=1").Scan(&migrated))
	require.JSONEq(t, `{"modules":{"accounts":"view","users":"manage"},"flags":["data.export","users.balance_adjust"]}`, string(migrated))
	permissions, err := operatoraccess.DecodeWire(migrated)
	require.NoError(t, err)
	require.True(t, permissions.Can(operatoraccess.ModuleAccounts, operatoraccess.LevelView))
	require.True(t, permissions.Can(operatoraccess.ModuleUsers, operatoraccess.LevelManage))
	require.True(t, permissions.Has(operatoraccess.FlagDataExport))
	require.True(t, permissions.Has(operatoraccess.FlagUsersBalanceAdjust))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT permissions FROM admin_operator_permissions WHERE user_id=2").Scan(&unchanged))
	require.JSONEq(t, untouched, string(unchanged))

	result, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	updated, err = result.RowsAffected()
	require.NoError(t, err)
	require.Zero(t, updated, "replaying the migration leaves every row untouched")
}
