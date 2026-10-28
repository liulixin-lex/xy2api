//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func operatorRepositoryUser(t *testing.T, role string) *service.User {
	t.Helper()
	u := mustCreateUser(t, testEntClient(t), &service.User{Role: role})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID) })
	return u
}
func TestAdminOperatorPermissionRepoUpsertAndGet(t *testing.T) {
	ctx := context.Background()
	u := operatorRepositoryUser(t, service.RoleOperator)
	actor := operatorRepositoryUser(t, service.RoleAdmin)
	repo := NewAdminOperatorPermissionRepository(integrationDB)
	raw, found, err := repo.Get(ctx, u.ID)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, raw)
	require.NoError(t, repo.Upsert(ctx, u.ID, []byte(`{"modules":{"accounts":"view"},"flags":[]}`), actor.ID))
	raw, found, err = repo.Get(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, `{"modules":{"accounts":"view"},"flags":[]}`, string(raw))
	var updatedBy int64
	var before time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT updated_by,updated_at FROM admin_operator_permissions WHERE user_id=$1`, u.ID).Scan(&updatedBy, &before))
	require.Equal(t, actor.ID, updatedBy)
	require.NoError(t, repo.Upsert(ctx, u.ID, []byte(`{"modules":{"usage":"manage"},"flags":["data.export"]}`), u.ID))
	raw, found, err = repo.Get(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, `{"modules":{"usage":"manage"},"flags":["data.export"]}`, string(raw))
	var after time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT updated_by,updated_at FROM admin_operator_permissions WHERE user_id=$1`, u.ID).Scan(&updatedBy, &after))
	require.Equal(t, u.ID, updatedBy)
	require.False(t, after.Before(before))
	_, err = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID)
	require.NoError(t, err)
	_, found, err = repo.Get(ctx, u.ID)
	require.NoError(t, err)
	require.False(t, found, "hard deletion cascades the permission row")
}
