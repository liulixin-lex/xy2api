//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestAdminOperatorPermissionsMigrationRollbackAndReupgrade(t *testing.T) {
	// TestMain initializes an empty database through ApplyMigrations. The test
	// uses that real schema and exercises the documented transactional rollback.
	ctx := context.Background()
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	u := operatorRepositoryUser(t, service.RoleOperator)
	repo := NewAdminOperatorPermissionRepository(integrationDB)
	require.NoError(t, repo.Upsert(ctx, u.ID, []byte(`{"modules":{"usage":"view"},"flags":[]}`), u.ID))
	raw, err := migrations.FS.ReadFile("282_admin_operator_permissions.sql")
	require.NoError(t, err)
	statements := []string{}
	inside := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "--"))
		if line == "BEGIN;" {
			inside = true
		}
		if inside {
			statements = append(statements, line)
		}
		if line == "COMMIT;" {
			break
		}
	}
	require.Len(t, statements, 5)
	_, err = integrationDB.ExecContext(ctx, strings.Join(statements, "\n"))
	require.NoError(t, err)
	var role string
	require.NoError(t, integrationDB.QueryRow(`SELECT role FROM users WHERE id=$1`, u.ID).Scan(&role))
	require.Equal(t, service.RoleUser, role)
	var exists bool
	require.NoError(t, integrationDB.QueryRow(`SELECT to_regclass('public.admin_operator_permissions') IS NOT NULL`).Scan(&exists))
	require.False(t, exists)
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE filename='282_admin_operator_permissions.sql'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM admin_operator_permissions`).Scan(&count))
	require.Zero(t, count)
	var checksum string
	require.NoError(t, integrationDB.QueryRow(`SELECT checksum FROM schema_migrations WHERE filename='282_admin_operator_permissions.sql'`).Scan(&checksum))
	require.Equal(t, "eeef08381e24d4a9d33af083b3b62b433d68ac505f1e9e196538c764124321f1", checksum)
}
